package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/IliaSA101/aiexport/internal/bundle"
	"github.com/IliaSA101/aiexport/internal/ignore"
	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/paths"
	"github.com/IliaSA101/aiexport/internal/plan"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

// listFlag — флаг, который можно повторять: --src ... --src ...
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, "; ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// parseSource разбирает «режим|подпись|путь». Разделитель «|», а не «:»: в путях
// Windows двоеточие стоит после буквы диска. Путь берётся последним, поэтому «|»
// допустим и в нём.
func parseSource(s string) (model.Source, error) {
	parts := strings.SplitN(s, "|", 3)
	if len(parts) != 3 {
		return model.Source{}, fmt.Errorf("источник %q: ожидается формат режим|подпись|путь", s)
	}
	mode := model.Mode(strings.TrimSpace(parts[0]))
	if !mode.Valid() {
		return model.Source{}, fmt.Errorf("источник %q: неизвестный режим %q (допустимы full и tree)", s, parts[0])
	}
	path := strings.TrimSpace(parts[2])
	if path == "" {
		return model.Source{}, fmt.Errorf("источник %q: пустой путь", s)
	}
	// Тот же валидатор, что у сервера и плана: filepath.Abs сам по себе
	// «съедал» префикс NT-пути, и защита держалась на случайности.
	abs, err := paths.Clean(path, true)
	if err != nil {
		return model.Source{}, fmt.Errorf("источник %q: путь %q: %w", s, path, err)
	}
	return model.Source{Path: abs, Label: strings.TrimSpace(parts[1]), Mode: mode}, nil
}

// usageErr печатает причину в вывод флагов и возвращает errUsage: main по этому
// признаку молча выходит с кодом 2, поэтому текст должен быть напечатан здесь.
func usageErr(fs *flag.FlagSet, err error) error {
	_, _ = fmt.Fprintln(fs.Output(), err)
	return fmt.Errorf("%w: %w", errUsage, err)
}

// runExport — подкоманда `export`: то же, что делает UI, но без браузера.
func runExport(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("aiexport export", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var srcs, excludes listFlag
	fs.Var(&srcs, "src", `источник в формате режим|подпись|путь (режим full или tree); можно повторять`)
	out := fs.String("out", "", "файл бандла (обязательно)")
	force := fs.Bool("force", false, "перезаписать --out, если файл уже существует")
	noGit := fs.Bool("no-gitignore", false, "не читать .gitignore в деревьях источников")
	fs.Var(&excludes, "exclude", `маска исключения, например "**/*.min.js"; можно повторять`)
	maxSize := fs.Int64("max-size", 0, "лимит размера файла в байтах (0 — 262144)")
	instr := fs.String("instructions", "", "свободный текст в конец бандла")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() > 0 {
		return usageErr(fs, fmt.Errorf("лишние аргументы: %s", strings.Join(fs.Args(), " ")))
	}
	if len(srcs) == 0 {
		return usageErr(fs, errors.New("нужен хотя бы один --src"))
	}
	if *out == "" {
		return usageErr(fs, errors.New("нужен --out"))
	}

	spec := model.Spec{
		Exclude:      excludes,
		UseGitignore: !*noGit,
		MaxFileSize:  *maxSize,
		Instructions: *instr,
	}
	for _, s := range srcs {
		src, err := parseSource(s)
		if err != nil {
			return usageErr(fs, err)
		}
		spec.Sources = append(spec.Sources, src)
	}
	for _, g := range spec.Exclude {
		if err := ignore.ValidateGlob(g); err != nil {
			return usageErr(fs, err)
		}
	}
	// Путь вывода проходит тот же валидатор, что и источники: иначе --out на UNC
	// отправил бы собранный код (и NTLM-хэш) на чужой сервер, а имя вроде con или
	// LPT9.md создало бы файл, который Проводник не открывает и не удаляет.
	outAbs, err := paths.Clean(*out, true)
	if err != nil {
		return usageErr(fs, fmt.Errorf("путь --out %q: %w", *out, err))
	}
	if err := checkOutFree(outAbs, *force); err != nil {
		return usageErr(fs, err)
	}
	// Без этого повторный запуск включил бы в бандл собственный предыдущий результат.
	spec.Exclude = append(spec.Exclude, plan.ExcludeOutput(spec.Sources, outAbs)...)

	counter, err := tokens.NewCounter()
	if err != nil {
		return err
	}
	p, err := plan.Build(ctx, counter, spec)
	if err != nil {
		return fmt.Errorf("построение плана: %w", err)
	}
	// Повторная проверка: сборка шла долго, файл мог появиться за это время.
	if err := checkOutFree(outAbs, *force); err != nil {
		return err
	}
	if err := bundle.WriteFile(ctx, p, outAbs); err != nil {
		return fmt.Errorf("запись бандла: %w", err)
	}
	return printLine(stdout, fmt.Sprintf("Готово: файлов %d, токенов %d (зона %s), только деревом %d, исключено записей %d -> %s",
		p.Totals.Included, p.Totals.Tokens, p.Totals.Zone, p.Totals.TreeOnly, p.Totals.Skipped, outAbs))
}

// checkOutFree не даёт молча заменить существующий файл пользователя: бандл
// пишется атомарной подменой, и без этой проверки `--out important.md` стёр бы
// чужие данные с кодом выхода 0. Каталог вместо файла отвергается всегда.
func checkOutFree(path string, force bool) error {
	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("проверка --out %q: %w", path, err)
	case st.IsDir():
		return fmt.Errorf("--out %q: это папка, нужно имя файла", path)
	case !force:
		return fmt.Errorf("--out %q уже существует; чтобы заменить файл, добавьте --force", path)
	}
	return nil
}
