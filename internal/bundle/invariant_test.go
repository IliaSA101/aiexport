package bundle

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/testkit"
)

// Главный инвариант продукта: файл не может числиться в бандле одним именем без
// содержимого. Манифест, секции с непустым фенсом и раздел пропущенного обязаны
// сходиться между собой и с планом. Это самый дешёвый тест против всего класса
// «файл есть, содержимого нет» (молчаливая замена байт, потерянная секция и т.п.).
func TestBundleInvariantManifestMatchesContents(t *testing.T) {
	proj, tree := t.TempDir(), t.TempDir()
	testkit.WriteString(t, proj, "main.go", "package main\n\nfunc main() {}\n")
	testkit.WriteString(t, proj, "docs/readme.md", "# Заголовок\nТекст с ``` внутри\n")
	testkit.Write(t, proj, "notes/utf16.txt", testkit.UTF16("заметка в UTF-16\n", false))
	testkit.WriteString(t, proj, "crlf.txt", "a\r\nb\r\n")
	// Ловушки: ни одна из этих записей не должна появиться в бандле «пустой».
	testkit.Write(t, proj, "legacy.php", []byte("<?php\n// \xCA\xEE\xEC\xEC\xE5\xED\xF2\n$x = \"\xC7\xED\xE0\xF7\";\n"))
	testkit.Write(t, proj, "late-legacy.php", append([]byte(strings.Repeat("a", 2000)), 0xCF, 0xF0, '\n'))
	testkit.Write(t, proj, "blob.bin", []byte("MZ\x00\x00"))
	testkit.Write(t, proj, "late-nul.txt", append([]byte(strings.Repeat("a", 2000)), 0, 'x'))
	testkit.WriteString(t, proj, "huge.txt", strings.Repeat("0123456789abcdef\n", 64*1024))
	testkit.WriteString(t, proj, "app.log", "лог")
	testkit.WriteString(t, proj, ".gitignore", "*.log\n")
	testkit.WriteString(t, proj, "node_modules/x.js", "x")
	testkit.WriteString(t, tree, "a.md", "# A\n")
	testkit.WriteString(t, tree, "sub/b.md", "# B\n")

	p := buildPlan(t, model.Spec{
		Sources: []model.Source{
			{Path: proj, Label: "Проект", Mode: model.ModeFull},
			{Path: tree, Label: "Схемы", Mode: model.ModeTree},
		},
		UseGitignore: true,
	})
	out := render(t, p)

	manifest, sections := parseBundle(t, out)
	sizeByKey := map[string]int64{}
	for _, f := range p.Files {
		sizeByKey[fmt.Sprintf("[%s] %s", p.Spec.Sources[f.Source].Label, f.Rel)] = f.Size
	}

	// 0. План сходится с диском: тихо выброшенная при обходе запись (например,
	// бинарная) не должна оставаться незамеченной, раз бандл сверяется с планом.
	onDisk := 0
	for _, root := range []string{proj, tree} {
		werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return fs.SkipDir
			}
			if !d.IsDir() {
				onDisk++
			}
			return nil
		})
		if werr != nil {
			t.Fatal(werr)
		}
	}
	// node_modules в плане — одна запись-каталог без файлов внутри, поэтому её
	// считаем отдельно: на диске она пропущена целиком.
	nodeModules := 0
	for _, f := range p.Files {
		if f.Dir {
			nodeModules++
		}
	}
	if onDisk+nodeModules != len(p.Files) {
		t.Errorf("на диске %d файлов и %d отсечённых каталогов, в плане %d записей", onDisk, nodeModules, len(p.Files))
	}

	// 1. Строк манифеста столько же, сколько секций с содержимым.
	if len(manifest) != len(sections) {
		t.Fatalf("манифест: %d строк, секций с содержимым: %d\n%s", len(manifest), len(sections), out)
	}
	for i, m := range manifest {
		if m != sections[i].key {
			t.Errorf("строка манифеста %d = %q, секция = %q", i+1, m, sections[i].key)
		}
		// Законный пустой файл даёт пустой фенс; проверяем только непустые по плану.
		if strings.TrimSpace(sections[i].body) == "" && sizeByKey[m] != 0 {
			t.Errorf("секция %q: фенс пустой", m)
		}
		if strings.ContainsRune(sections[i].body, 0xFFFD) {
			t.Errorf("секция %q: в содержимом U+FFFD (молчаливая замена байт)", m)
		}
	}

	// 2. Каждый файл плана — либо в манифесте, либо в разделе пропущенного.
	inManifest := map[string]bool{}
	for _, m := range manifest {
		inManifest[m] = true
	}
	skippedByReason := map[model.Reason]int{}
	for _, f := range p.Files {
		key := fmt.Sprintf("[%s] %s", p.Spec.Sources[f.Source].Label, f.Rel)
		if f.Status == model.StatusIncluded {
			if !inManifest[key] {
				t.Errorf("включённого файла %q нет в манифесте", key)
			}
			continue
		}
		if inManifest[key] {
			t.Errorf("пропущенный файл %q попал в манифест", key)
		}
		if f.Reason == model.ReasonNone {
			t.Errorf("пропущенный файл %q без причины", key)
		}
		skippedByReason[f.Reason]++
	}
	if len(inManifest) != p.Totals.Included {
		t.Errorf("в манифесте %d, в итогах плана %d", len(inManifest), p.Totals.Included)
	}
	for reason, n := range skippedByReason {
		want := fmt.Sprintf("(%s): %d", reason, n)
		if !strings.Contains(out, want) {
			t.Errorf("в разделе пропущенного нет строки %q", want)
		}
	}

	// Шапка: три числа, сходящиеся с планом; источник-дерево не выглядит пустой выгрузкой.
	head := fmt.Sprintf("- Файлов: с содержимым — %d, только деревом — %d, исключено — %d\n",
		p.Totals.Included, p.Totals.TreeOnly, p.Totals.Skipped)
	if p.Totals.TreeOnly != 2 || !strings.Contains(out, head) {
		t.Errorf("шапка не содержит %q (treeOnly=%d)", head, p.Totals.TreeOnly)
	}

	// 3. Файлы не в UTF-8 видны в отчёте с причиной, а не включены.
	if skippedByReason[model.ReasonEncoding] != 2 {
		t.Errorf("файлов с причиной encoding %d, ожидали 2 (legacy.php и late-legacy.php)", skippedByReason[model.ReasonEncoding])
	}
	for _, name := range []string{"legacy.php", "late-legacy.php"} {
		if inManifest["[Проект] "+name] {
			t.Errorf("%s в cp1251 попал в манифест", name)
		}
	}
	if !strings.Contains(out, "(encoding): 2") || !strings.Contains(out, "перекодировка не выполняется") {
		t.Errorf("причина encoding не объяснена в отчёте:\n%s", out)
	}
	if strings.Contains(out, "Ничего: все найденные файлы включены") {
		t.Error("при пропущенных файлах нельзя заявлять, что включено всё")
	}
}

type section struct{ key, body string }

var manifestLine = regexp.MustCompile(`^\d+\. (\[[^\]]*\] .+?) \(\d+ токенов\)$`)

// parseBundle разбирает бандл: ключи «[подпись] путь» из манифеста и секции
// раздела «Файлы» с непустым фенсом. Секция с «ВНИМАНИЕ» фенса не имеет и
// в список не попадает — расхождение с манифестом тогда проваливает тест.
func parseBundle(t *testing.T, out string) (manifest []string, sections []section) {
	t.Helper()
	lines := strings.Split(out, "\n")
	zone := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "## ") && !strings.HasPrefix(l, "## [") {
			zone = l
			continue
		}
		switch {
		case zone == "## Манифест":
			if m := manifestLine.FindStringSubmatch(l); m != nil {
				manifest = append(manifest, m[1])
			}
		case zone == "## Файлы" && strings.HasPrefix(l, "## ["):
			key := strings.TrimPrefix(l, "## ")
			// Заголовок секции, пустая строка, открывающий фенс.
			if i+2 >= len(lines) || !strings.HasPrefix(lines[i+2], "```") {
				continue
			}
			fence := lines[i+2]
			j := i + 3
			var body []string
			for ; j < len(lines) && lines[j] != strings.TrimRight(fence, "abcdefghijklmnopqrstuvwxyz0123456789+-_"); j++ {
				body = append(body, lines[j])
			}
			sections = append(sections, section{key: key, body: strings.Join(body, "\n")})
			i = j
		}
	}
	return manifest, sections
}
