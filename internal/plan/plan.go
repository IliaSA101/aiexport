// Пакет plan строит model.Plan из model.Spec одним вызовом: обход всех источников
// и подсчёт токенов. Им пользуются и CLI, и HTTP API — логика не дублируется.
package plan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/IliaSA101/aiexport/internal/ignore"
	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/paths"
	"github.com/IliaSA101/aiexport/internal/scan"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

// Потолки на входные данные: Spec приходит из UI и конфигов, ему нельзя верить.
const (
	maxSources = 64
	maxExclude = 1000
	// memoryBudget — сколько «заряженных» байт одновременно держат воркеры
	// подсчёта токенов. Заряжается не размер файла, а его пик обработки
	// (см. processingFactor).
	memoryBudget = 64 << 20
	maxWorkers   = 8
	// processingFactor — во сколько раз пик памяти на файл выше его размера:
	// сырые байты, string(data), замена CRLF, а главное — аллокации токенизатора
	// (срезы токенов и кусков текста). Замер аудита: 16 файлов по 8 МБ при бюджете
	// 64 МБ «по размеру» дали пик 3,3-3,8 ГБ, то есть десятки размеров на файл;
	// множитель берётся с запасом, чтобы бюджет реально ограничивал память.
	processingFactor = 32
	// processingBase — фиксированная добавка на файл: структуры чтения и токенизатора
	// у мелких файлов не равны нулю.
	processingBase = 16 << 10
)

// Normalize проверяет Spec и возвращает его копию с подставленными значениями по
// умолчанию (подписи, лимит размера). Ошибка — всегда про ввод пользователя.
func Normalize(spec model.Spec) (model.Spec, error) {
	if len(spec.Sources) == 0 {
		return spec, errors.New("не задан ни один источник")
	}
	if len(spec.Sources) > maxSources {
		return spec, fmt.Errorf("источников %d, максимум %d", len(spec.Sources), maxSources)
	}
	if len(spec.Exclude) > maxExclude {
		return spec, fmt.Errorf("масок исключения %d, максимум %d", len(spec.Exclude), maxExclude)
	}
	out := spec
	out.Sources = make([]model.Source, len(spec.Sources))
	for i, s := range spec.Sources {
		if !s.Mode.Valid() {
			return spec, fmt.Errorf("источник %d: неизвестный режим %q (допустимы full и tree)", i+1, s.Mode)
		}
		if strings.TrimSpace(s.Path) == "" {
			return spec, fmt.Errorf("источник %d: пустой путь", i+1)
		}
		// Единый валидатор путей: UNC и NT-пути отвергаются здесь, а не в обходе
		// (обращение к UNC отправляет NTLM-хэш на чужой сервер), и по белому списку
		// после нормализации, а не по префиксам сырой строки.
		clean, perr := paths.Clean(s.Path, false)
		if perr != nil {
			return spec, fmt.Errorf("источник %d: %w", i+1, perr)
		}
		// Неотменяемое правило .git работает от корня источника, поэтому сам каталог
		// .git (или что-то внутри него) выгрузил бы config с токеном в url.
		if hasGitSegment(clean) {
			return spec, fmt.Errorf("источник %d: путь содержит каталог .git — выгружать внутренности репозитория нельзя, укажите корень проекта", i+1)
		}
		s.Path = clean
		if strings.TrimSpace(s.Label) == "" {
			s.Label = filepath.Base(s.Path)
		}
		out.Sources[i] = s
	}
	for _, g := range spec.Exclude {
		if err := ignore.ValidateGlob(g); err != nil {
			return spec, err
		}
	}
	out.Exclude = append([]string(nil), spec.Exclude...)
	out.MaxFileSize = scan.ClampMaxFileSize(spec.MaxFileSize)
	return out, nil
}

// hasGitSegment — есть ли в пути звено .git (без учёта регистра: на Windows и
// macOS .GIT тот же каталог). Разделители режем оба, не завися от ОС.
func hasGitSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

// Build обходит все источники и считает токены включённых файлов.
// Счётчик создаётся один раз на процесс (загрузка словаря занимает время);
// nil допустим и означает «создать здесь».
func Build(ctx context.Context, c *tokens.Counter, spec model.Spec) (model.Plan, error) {
	spec, err := Normalize(spec)
	if err != nil {
		return model.Plan{}, err
	}
	// Отмену проверяем сразу: на пустом или чисто tree-источнике воркеров нет,
	// и без этого Ctrl+C вернул бы успешный план.
	if err := ctx.Err(); err != nil {
		return model.Plan{}, err
	}
	if c == nil {
		if c, err = tokens.NewCounter(); err != nil {
			return model.Plan{}, err
		}
	}

	lim := scan.Limits{MaxFileSize: spec.MaxFileSize, Exclude: spec.Exclude, UseGitignore: spec.UseGitignore}
	var files []model.FileEntry
	for i, src := range spec.Sources {
		entries, werr := scan.Walk(ctx, src, i, lim)
		if werr != nil {
			return model.Plan{}, werr
		}
		files = append(files, entries...)
	}

	roots := make([]*os.Root, len(spec.Sources))
	defer func() {
		for _, r := range roots {
			if r != nil {
				_ = r.Close()
			}
		}
	}()
	for i, src := range spec.Sources {
		r, oerr := os.OpenRoot(src.Path)
		if oerr != nil {
			return model.Plan{}, fmt.Errorf("открытие источника %q: %w", src.Label, oerr)
		}
		roots[i] = r
	}

	if err := countTokens(ctx, c, roots, files, spec.MaxFileSize); err != nil {
		return model.Plan{}, err
	}

	p := model.Plan{Spec: spec, Files: files, Created: time.Now()}
	for _, f := range files {
		if f.Reason == model.ReasonModeTree {
			p.Totals.TreeOnly++
			continue
		}
		if f.Status != model.StatusIncluded {
			p.Totals.Skipped++
			continue
		}
		p.Totals.Included++
		p.Totals.Bytes += f.Size
		p.Totals.Tokens += f.Tokens
	}
	p.Totals.Zone = tokens.Zone(p.Totals.Tokens)
	return p, nil
}

// countTokens читает включённые файлы и проставляет Tokens. Файл, который при
// полном чтении оказался бинарным или вырос за лимит, переводится в пропущенные
// с причиной: окно между обходом и чтением реально, и молча терять файл нельзя.
func countTokens(ctx context.Context, c *tokens.Counter, roots []*os.Root, files []model.FileEntry, maxSize int64) error {
	var idx []int
	for i := range files {
		if files[i].Status == model.StatusIncluded {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	bud := newBudget(memoryBudget)
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	fail := func(err error) {
		once.Do(func() { firstErr = err; cancel() })
	}

	for range min(runtime.GOMAXPROCS(0), maxWorkers, len(idx)) {
		wg.Go(func() {
			for i := range jobs {
				if err := countOne(ctx, c, bud, roots, &files[i], maxSize); err != nil {
					fail(err)
					return
				}
			}
		})
	}
feed:
	for _, i := range idx {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func countOne(ctx context.Context, c *tokens.Counter, bud *budget, roots []*os.Root, e *model.FileEntry, maxSize int64) error {
	held, err := bud.acquire(ctx, e.Size*processingFactor+processingBase)
	if err != nil {
		return err
	}
	defer bud.release(held)

	text, size, rerr := scan.ReadText(roots[e.Source], e.Rel, maxSize)
	switch {
	case errors.Is(rerr, scan.ErrBinary):
		skip(e, model.ReasonBinary, "NUL-байт в содержимом")
	case errors.Is(rerr, scan.ErrNotUTF8):
		skip(e, model.ReasonEncoding, scan.NotUTF8RuleText)
	case errors.Is(rerr, scan.ErrTooBig):
		skip(e, model.ReasonTooBig, fmt.Sprintf("файл вырос после обхода: больше лимита %d", maxSize))
	case rerr != nil:
		skip(e, model.ReasonUnreadable, rerr.Error())
	default:
		e.Size = size
		e.Tokens = c.Count(text)
	}
	return ctx.Err()
}

func skip(e *model.FileEntry, r model.Reason, rule string) {
	e.Status = model.StatusSkipped
	e.Reason = r
	e.Rule = rule
	e.Tokens = 0
}
