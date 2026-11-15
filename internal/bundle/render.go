// Пакет bundle — рендер Plan в Markdown потоком и атомарная запись результата в файл.
package bundle

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/scan"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

const examplesPerReason = 5

// out — обёртка над буферизованным писателем. У bufio.Writer ошибка «залипает»:
// после первого сбоя все записи возвращают её же, а Flush отдаёт наружу. Поэтому
// результаты отдельных записей игнорируются сознательно, ошибка проверяется один
// раз — в конце, во Flush.
type out struct{ bw *bufio.Writer }

func (o *out) p(format string, args ...any) { _, _ = fmt.Fprintf(o.bw, format, args...) }
func (o *out) s(s string)                   { _, _ = o.bw.WriteString(s) }

// Render пишет бандл потоком, не накапливая его в памяти: содержимое файлов
// читается по одному и сразу уходит в w. Источники в режиме tree не открываются.
func Render(ctx context.Context, p model.Plan, w io.Writer) error {
	// Отмена проверяется первой: без включённых файлов цикл содержимого не
	// выполняется, и файл записался бы после Ctrl+C.
	if err := ctx.Err(); err != nil {
		return err
	}
	roots, err := openRoots(p)
	if err != nil {
		return err
	}
	defer func() {
		for _, r := range roots {
			if r != nil {
				_ = r.Close()
			}
		}
	}()

	o := &out{bw: bufio.NewWriterSize(w, 64*1024)}
	if err := writeHead(ctx, o, p); err != nil {
		return err
	}
	writeSkipped(o, p)
	if err := writeContents(ctx, o, p, roots); err != nil {
		return err
	}
	writeTail(o, p)
	if err := o.bw.Flush(); err != nil {
		return fmt.Errorf("запись бандла: %w", err)
	}
	return nil
}

// openRoots открывает корни только тех источников, у которых есть включённые файлы.
func openRoots(p model.Plan) ([]*os.Root, error) {
	roots := make([]*os.Root, len(p.Spec.Sources))
	need := make([]bool, len(roots))
	for _, f := range p.Files {
		if f.Status == model.StatusIncluded && f.Source >= 0 && f.Source < len(need) {
			need[f.Source] = true
		}
	}
	for i, ok := range need {
		if !ok {
			continue
		}
		r, err := os.OpenRoot(p.Spec.Sources[i].Path)
		if err != nil {
			for _, opened := range roots {
				if opened != nil {
					_ = opened.Close()
				}
			}
			return nil, fmt.Errorf("открытие источника %q: %w", p.Spec.Sources[i].Label, err)
		}
		roots[i] = r
	}
	return roots, nil
}

func label(p model.Plan, i int) string {
	if i < 0 || i >= len(p.Spec.Sources) {
		return "?"
	}
	return clean(p.Spec.Sources[i].Label)
}

// writeHead пишет шапку, манифест и деревья.
func writeHead(ctx context.Context, o *out, p model.Plan) error {
	var included []model.FileEntry
	var total, treeOnly, excluded int
	for _, f := range p.Files {
		switch {
		case f.Status == model.StatusIncluded:
			included = append(included, f)
			total += f.Tokens
		case f.Reason == model.ReasonModeTree:
			treeOnly++
		default:
			excluded++
		}
	}

	o.s("# Бандл aiexport\n\n")
	if !p.Created.IsZero() {
		o.p("- Дата: %s\n", p.Created.Format("2006-01-02 15:04:05 -07:00"))
	}
	o.p("- Источников: %d\n", len(p.Spec.Sources))
	// Три числа вместо одного «включено»: источник только деревом иначе давал
	// «Файлов включено: 0» и выглядел как пустая выгрузка, хотя дерево в бандле есть.
	o.p("- Файлов: с содержимым — %d, только деревом — %d, исключено — %d\n", len(included), treeOnly, excluded)
	o.p("- Токенов (o200k_base, только содержимое файлов): %d, зона: %s\n\n", total, tokens.Zone(total))

	// Манифест — инструмент проверки: читатель сверяет «сколько файлов тут и все ли
	// у меня есть». Без него молчаливая потеря файла не обнаруживается.
	o.s("## Манифест\n\n")
	if len(included) == 0 {
		o.s("Ни одного файла с содержимым нет.\n")
	}
	for i, f := range included {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		o.p("%d. [%s] %s (%d токенов)\n", i+1, label(p, f.Source), clean(f.Rel), f.Tokens)
	}

	o.s("\n## Дерево\n\n")
	for i, src := range p.Spec.Sources {
		root := newNode(label(p, i), true)
		treeMode := src.Mode == model.ModeTree
		for _, f := range p.Files {
			if f.Source == i && inTree(f) {
				root.insert(f, treeMode)
			}
		}
		root.sort()
		o.p("### [%s] (режим: %s)\n\n", label(p, i), src.Mode)
		if treeMode {
			o.s("Режим tree: содержимое файлов этого источника не включено намеренно — только дерево.\n\n")
		}
		o.s("```text\n")
		o.p("%s/\n", root.name)
		root.print(o, "")
		o.s("```\n\n")
	}
	return nil
}

// reasonOrder задаёт порядок строк в разделе пропущенного. Порядок фиксирован,
// чтобы вывод не зависел от порядка обхода.
func reasonOrder() []model.Reason {
	return []model.Reason{
		model.ReasonDefault, model.ReasonGitignore, model.ReasonGlob, model.ReasonModeTree,
		model.ReasonBinary, model.ReasonTooBig, model.ReasonSymlink, model.ReasonTooDeep,
		model.ReasonEncoding, model.ReasonUnreadable,
	}
}

func reasonTitle(r model.Reason) string {
	switch r {
	case model.ReasonDefault:
		return "исключено правилами по умолчанию"
	case model.ReasonGitignore:
		return "исключено правилами .gitignore"
	case model.ReasonGlob:
		return "исключено пользовательскими масками"
	case model.ReasonModeTree:
		return "режим tree: содержимое не включено намеренно"
	case model.ReasonBinary:
		return "бинарные файлы"
	case model.ReasonTooBig:
		return "файл больше лимита размера"
	case model.ReasonSymlink:
		return "символические ссылки на каталоги (не разворачиваются)"
	case model.ReasonTooDeep:
		return "каталоги глубже лимита обхода"
	case model.ReasonEncoding:
		return "файлы не в UTF-8 (перекодировка не выполняется)"
	case model.ReasonUnreadable:
		return "не удалось прочитать"
	}
	return string(r)
}

// writeSkipped — раздел «что сознательно не включено». Без него модель считает
// отсутствующий каталог несуществующим, а не исключённым.
func writeSkipped(o *out, p model.Plan) {
	byReason := map[model.Reason][]model.FileEntry{}
	for _, f := range p.Files {
		if f.Status == model.StatusSkipped {
			byReason[f.Reason] = append(byReason[f.Reason], f)
		}
	}
	o.s("## Что сознательно не включено\n\n")
	if len(byReason) == 0 {
		o.s("Ничего: все найденные файлы включены.\n\n")
		return
	}
	known := map[model.Reason]bool{}
	reasons := reasonOrder()
	for _, r := range reasons {
		known[r] = true
	}
	for r := range byReason {
		if !known[r] {
			reasons = append(reasons, r) // незнакомая причина не должна пропасть
		}
	}
	for _, r := range reasons {
		list := byReason[r]
		if len(list) == 0 {
			continue
		}
		dirs := 0
		for _, f := range list {
			if f.Dir {
				dirs++
			}
		}
		o.p("- %s (%s): %d", reasonTitle(r), r, len(list))
		if dirs > 0 {
			o.p(" (каталогов целиком: %d)", dirs)
		}
		o.s("\n")
		if r == model.ReasonModeTree {
			writeTreeModeCounts(o, p, list)
			continue
		}
		// Каталоги показываем первыми: один «vendor/» говорит больше десяти файлов.
		shown := 0
		for pass := 0; pass < 2 && shown < examplesPerReason; pass++ {
			for _, f := range list {
				if f.Dir != (pass == 0) || shown >= examplesPerReason {
					continue
				}
				name := clean(f.Rel)
				if f.Dir {
					name += "/"
				}
				if f.Rule != "" {
					o.p("  - [%s] %s — правило: %s\n", label(p, f.Source), name, clean(f.Rule))
				} else {
					o.p("  - [%s] %s\n", label(p, f.Source), name)
				}
				shown++
			}
		}
		if len(list) > shown {
			o.p("  - … и ещё %d\n", len(list)-shown)
		}
	}
	o.s("\n")
}

func writeTreeModeCounts(o *out, p model.Plan, list []model.FileEntry) {
	counts := map[int]int{}
	for _, f := range list {
		counts[f.Source]++
	}
	for i := range p.Spec.Sources {
		if n := counts[i]; n > 0 {
			o.p("  - [%s]: файлов %d\n", label(p, i), n)
		}
	}
}

// writeContents пишет содержимое включённых файлов.
func writeContents(ctx context.Context, o *out, p model.Plan, roots []*os.Root) error {
	o.s("## Файлы\n\n")
	maxSize := scan.ClampMaxFileSize(p.Spec.MaxFileSize)
	n := 0
	for _, f := range p.Files {
		if f.Status != model.StatusIncluded {
			continue
		}
		n++
		if err := ctx.Err(); err != nil {
			return err
		}
		o.p("## [%s] %s\n\n", label(p, f.Source), clean(f.Rel))

		var root *os.Root
		if f.Source >= 0 && f.Source < len(roots) {
			root = roots[f.Source]
		}
		if root == nil {
			o.s("ВНИМАНИЕ: источник недоступен, содержимое не включено.\n\n")
			continue
		}
		text, size, err := scan.ReadText(root, f.Rel, maxSize)
		if err != nil {
			// Файл не читается — это видно в бандле, а не молча пропадает.
			o.p("ВНИМАНИЕ: содержимое не включено: %s\n\n", clean(err.Error()))
			continue
		}
		if size != f.Size {
			o.p("ВНИМАНИЕ: размер файла изменился после построения плана (было %d, стало %d байт); содержимое актуально на момент сборки, число токенов в манифесте — нет.\n\n", f.Size, size)
		}
		writeFenced(o, langFor(f.Rel), text)
	}
	if n == 0 {
		o.s("Файлов с содержимым нет.\n\n")
	}
	return nil
}

// writeFenced оборачивает текст в фенс. Если внутри встречается ряд обратных
// апострофов, фенс делается длиннее самого длинного ряда — иначе бандл рвётся.
func writeFenced(o *out, lang, text string) {
	fence := strings.Repeat("`", max(3, longestBacktickRun(text)+1))
	o.p("%s%s\n", fence, lang)
	o.s(text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		o.s("\n")
	}
	o.p("%s\n\n", fence)
}

func longestBacktickRun(s string) int {
	best, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

// writeTail пишет явный список tree-источников и пользовательские инструкции.
func writeTail(o *out, p model.Plan) {
	var treeSrc []string
	for i, s := range p.Spec.Sources {
		if s.Mode == model.ModeTree {
			treeSrc = append(treeSrc, label(p, i))
		}
	}
	if len(treeSrc) > 0 {
		o.s("## Источники только деревом\n\n")
		o.s("Содержимое файлов этих источников не включено намеренно (режим tree); структура — в разделе «Дерево».\n\n")
		for _, l := range treeSrc {
			o.p("- [%s]\n", l)
		}
		o.s("\n")
	}
	if text := strings.TrimSpace(p.Spec.Instructions); text != "" {
		o.s("## Инструкции\n\n")
		o.s(text)
		o.s("\n")
	}
}
