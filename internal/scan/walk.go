// Пакет scan — обход файловой системы источника: лимиты, правила исключения,
// различение текста и бинаря. Содержимое файлов обход не хранит, только
// классифицирует.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/IliaSA101/aiexport/internal/ignore"
	"github.com/IliaSA101/aiexport/internal/model"
)

// Лимиты по умолчанию и жёсткие потолки. Числа приходят из конфига и UI —
// им нельзя доверять: без потолка «--max-size 99999999999» отключил бы защиту.
const (
	DefaultMaxFiles    = 200_000
	DefaultMaxDepth    = 64
	HardMaxFileSize    = 16 << 20
	hardMaxFiles       = 2_000_000
	hardMaxDepth       = 256
	ctxCheckEvery      = 64
	gitignoreName      = ".gitignore"
	notRegularRuleText = "не обычный файл (сокет, устройство, канал, точка соединения или другой reparse point)"
)

// ErrTooManyFiles — обход упёрся в потолок числа записей. Усечённый результат
// без ошибки был бы тихой потерей файлов.
var ErrTooManyFiles = errors.New("слишком много файлов")

// Limits — ограничения обхода и правила исключения. Правила лежат здесь же, а не
// отдельным аргументом: сигнатура Walk из спеки остаётся прежней.
type Limits struct {
	MaxFileSize  int64 // 0 -> model.DefaultMaxFileSize
	MaxFiles     int   // 0 -> DefaultMaxFiles; число записей, включая отсечённые каталоги
	MaxDepth     int   // 0 -> DefaultMaxDepth
	Exclude      []string
	UseGitignore bool
}

// ClampMaxFileSize приводит лимит размера к допустимому диапазону.
func ClampMaxFileSize(v int64) int64 {
	switch {
	case v <= 0:
		return model.DefaultMaxFileSize
	case v > HardMaxFileSize:
		return HardMaxFileSize
	}
	return v
}

func (l Limits) normalized() Limits {
	l.MaxFileSize = ClampMaxFileSize(l.MaxFileSize)
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	l.MaxFiles = min(l.MaxFiles, hardMaxFiles)
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	l.MaxDepth = min(l.MaxDepth, hardMaxDepth)
	return l
}

// walker держит состояние одного обхода.
type walker struct {
	ctx     context.Context
	root    *os.Root
	src     model.Source
	idx     int
	lim     Limits
	matcher *ignore.Matcher
	out     []model.FileEntry
	visited int
}

// Walk обходит источник и возвращает по записи на каждый файл и на каждый
// отсечённый целиком каталог. Ошибка чтения отдельного файла обход не валит:
// файл попадает в список как unreadable с текстом причины.
func Walk(ctx context.Context, src model.Source, idx int, lim Limits) ([]model.FileEntry, error) {
	lim = lim.normalized()
	st, err := os.Stat(src.Path)
	if err != nil {
		return nil, fmt.Errorf("источник %q: %w", src.Label, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("источник %q: %s не каталог", src.Label, src.Path)
	}
	// Всё чтение внутри источника идёт через os.Root: он не даст выйти за корень
	// ни через «..», ни через симлинк, как бы ни выглядели имена в чужом дереве.
	root, err := os.OpenRoot(src.Path)
	if err != nil {
		return nil, fmt.Errorf("открытие корня источника %q: %w", src.Label, err)
	}
	defer func() { _ = root.Close() }()

	w := &walker{ctx: ctx, root: root, src: src, idx: idx, lim: lim, matcher: ignore.New(lim.Exclude)}
	if err := fs.WalkDir(root.FS(), ".", w.visit); err != nil {
		return nil, fmt.Errorf("обход источника %q: %w", src.Label, err)
	}
	slices.SortStableFunc(w.out, func(a, b model.FileEntry) int { return strings.Compare(a.Rel, b.Rel) })
	return w.out, nil
}

func (w *walker) add(e model.FileEntry) error {
	if len(w.out) >= w.lim.MaxFiles {
		return fmt.Errorf("%w: больше %d записей", ErrTooManyFiles, w.lim.MaxFiles)
	}
	e.Source = w.idx
	w.out = append(w.out, e)
	return nil
}

func (w *walker) visit(p string, d fs.DirEntry, err error) error {
	w.visited++
	if w.visited%ctxCheckEvery == 0 {
		if cerr := w.ctx.Err(); cerr != nil {
			return cerr
		}
	}
	if err != nil {
		if p == "." {
			return err
		}
		// Каталог или файл не прочитался: фиксируем и идём дальше.
		return w.add(unreadable(p, d, err))
	}
	if d.IsDir() {
		return w.visitDir(p)
	}
	return w.visitFile(p, d)
}

func unreadable(p string, d fs.DirEntry, err error) model.FileEntry {
	return model.FileEntry{
		Rel: p, Status: model.StatusSkipped, Reason: model.ReasonUnreadable,
		Rule: err.Error(), Dir: d != nil && d.IsDir(),
	}
}

func (w *walker) visitDir(p string) error {
	if p != "." {
		if strings.Count(p, "/")+1 > w.lim.MaxDepth {
			if err := w.add(model.FileEntry{
				Rel: p, Status: model.StatusSkipped, Reason: model.ReasonTooDeep, Dir: true,
				Rule: fmt.Sprintf("глубина больше %d", w.lim.MaxDepth),
			}); err != nil {
				return err
			}
			return fs.SkipDir
		}
		// Каталог отсекается целиком до рекурсии: иначе vendor и node_modules
		// были бы обойдены запись за записью, ради того чтобы потом все выбросить.
		if ign, reason, rule := w.matcher.MatchDir(p); ign {
			if err := w.add(model.FileEntry{
				Rel: p, Status: model.StatusSkipped, Reason: reason, Rule: rule, Dir: true,
			}); err != nil {
				return err
			}
			return fs.SkipDir
		}
	}
	if w.lim.UseGitignore {
		return w.loadGitignore(p)
	}
	return nil
}

// loadGitignore подключает .gitignore каталога p. Вызывается уже после проверки
// самого каталога и до его содержимого, поэтому порядок «родитель раньше
// вложенного» держится сам собой.
func (w *walker) loadGitignore(dir string) error {
	rel := gitignoreName
	dirRel := ""
	if dir != "." {
		rel = dir + "/" + gitignoreName
		dirRel = dir
	}
	st, err := w.root.Stat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && !st.Mode().IsRegular() {
		return nil // каталог с таким именем и тому подобное — не правила
	}
	if err == nil {
		err = w.addGitignoreFile(dirRel, rel)
	}
	if aerr := err; aerr != nil {
		// Нечитаемые правила — не повод молча включить всё подряд: фиксируем.
		return w.add(model.FileEntry{
			Rel: rel, Status: model.StatusSkipped, Reason: model.ReasonUnreadable,
			Rule: "не удалось прочитать правила: " + aerr.Error(),
		})
	}
	return nil
}

func (w *walker) addGitignoreFile(dirRel, rel string) error {
	f, err := w.root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // чтение: ошибка закрытия ничего не теряет
	return w.matcher.AddGitignore(dirRel, f)
}

func (w *walker) visitFile(p string, d fs.DirEntry) error {
	if ign, reason, rule := w.matcher.MatchFile(p); ign {
		return w.add(model.FileEntry{Rel: p, Status: model.StatusSkipped, Reason: reason, Rule: rule})
	}

	var info fs.FileInfo
	var err error
	if d.Type()&fs.ModeSymlink != 0 {
		// Stat через os.Root следует по ссылке, но не выпускает за корень:
		// ссылка наружу даст ошибку и попадёт в unreadable.
		info, err = w.root.Stat(p)
		if err == nil && info.IsDir() {
			// Каталоги по ссылкам не разворачиваем: так не бывает циклов и выхода за корень.
			return w.add(model.FileEntry{
				Rel: p, Status: model.StatusSkipped, Reason: model.ReasonSymlink, Dir: true,
				Rule: "символическая ссылка на каталог не разворачивается",
			})
		}
	} else {
		info, err = d.Info()
	}
	if err != nil {
		return w.add(unreadable(p, d, err))
	}
	if !info.Mode().IsRegular() {
		// Открывать нельзя: чтение канала или устройства может повиснуть навсегда.
		return w.add(model.FileEntry{
			Rel: p, Size: info.Size(), Status: model.StatusSkipped,
			Reason: model.ReasonUnreadable, Rule: notRegularRuleText,
		})
	}

	e := model.FileEntry{Rel: p, Size: info.Size(), Status: model.StatusSkipped}
	// Режим tree проверяется первым: файл, содержимое которого и так не нужно,
	// не должен выглядеть «слишком большим».
	if w.src.Mode == model.ModeTree {
		e.Reason = model.ReasonModeTree
		return w.add(e)
	}
	// Лимит проверяется по размеру из каталога, до открытия файла.
	if info.Size() > w.lim.MaxFileSize {
		e.Reason = model.ReasonTooBig
		e.Rule = fmt.Sprintf("размер %d байт больше лимита %d", info.Size(), w.lim.MaxFileSize)
		return w.add(e)
	}
	verdict, err := sniff(w.root, p)
	switch {
	case err != nil:
		e.Reason = model.ReasonUnreadable
		e.Rule = err.Error()
	case verdict == verdictBinary:
		e.Reason = model.ReasonBinary
		e.Rule = "NUL-байт в начале файла"
	case verdict == verdictNotUTF8:
		e.Reason = model.ReasonEncoding
		e.Rule = NotUTF8RuleText
	default:
		e.Status = model.StatusIncluded
	}
	return w.add(e)
}
