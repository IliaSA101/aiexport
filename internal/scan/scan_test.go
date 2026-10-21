package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/testkit"
)

func byRel(entries []model.FileEntry) map[string]model.FileEntry {
	m := make(map[string]model.FileEntry, len(entries))
	for _, e := range entries {
		m[e.Rel] = e
	}
	return m
}

func walkFull(t *testing.T, root string, lim Limits) []model.FileEntry {
	t.Helper()
	got, err := Walk(context.Background(), model.Source{Path: root, Label: "t", Mode: model.ModeFull}, 3, lim)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return got
}

func TestWalkTraps(t *testing.T) {
	root := t.TempDir()
	fx := testkit.ScanTree(t, root)
	if !fx.Loop {
		t.Log("ни симлинки, ни junction недоступны: ловушка цикла не проверена")
	}
	entries := byRel(walkFull(t, root, Limits{}))

	want := map[string]struct {
		status model.Status
		reason model.Reason
	}{
		"ok.txt":        {model.StatusIncluded, ""},
		"crlf.txt":      {model.StatusIncluded, ""},
		"nul.bin":       {model.StatusSkipped, model.ReasonBinary},
		"utf16le.txt":   {model.StatusIncluded, ""},
		"utf16be.txt":   {model.StatusIncluded, ""},
		"utf16nul.dat":  {model.StatusSkipped, model.ReasonBinary},
		"utf8bom.txt":   {model.StatusIncluded, ""},
		"cp1251.txt":    {model.StatusSkipped, model.ReasonEncoding},
		"empty.txt":     {model.StatusIncluded, ""},
		"big.txt":       {model.StatusSkipped, model.ReasonTooBig},
		"sub/nested.go": {model.StatusIncluded, ""},
	}
	if fx.Symlinks {
		want["sub/loop"] = struct {
			status model.Status
			reason model.Reason
		}{model.StatusSkipped, model.ReasonSymlink}
		want["link.txt"] = struct {
			status model.Status
			reason model.Reason
		}{model.StatusIncluded, ""}
	}
	if fx.Loop && !fx.Symlinks {
		// Цикл создан junction: os.Root его не разворачивает, запись — в unreadable.
		if e := entries["sub/loop"]; e.Status != model.StatusSkipped {
			t.Errorf("sub/loop (junction) не пропущен: %+v", e)
		}
		delete(entries, "sub/loop")
	}
	for rel, w := range want {
		e, ok := entries[rel]
		if !ok {
			t.Errorf("%s: записи нет в результате", rel)
			continue
		}
		if e.Status != w.status || e.Reason != w.reason {
			t.Errorf("%s: статус %s/%q, ожидали %s/%q (правило: %s)", rel, e.Status, e.Reason, w.status, w.reason, e.Rule)
		}
		if e.Source != 3 {
			t.Errorf("%s: индекс источника %d, ожидали 3", rel, e.Source)
		}
	}
	if len(entries) != len(want) {
		t.Errorf("записей %d, ожидали %d: %v", len(entries), len(want), entries)
	}
	if e := entries["big.txt"]; e.Size != 17*64*1024 {
		t.Errorf("размер big.txt = %d", e.Size)
	}
}

func TestWalkTreeMode(t *testing.T) {
	root := t.TempDir()
	testkit.ScanTree(t, root)
	got, err := Walk(context.Background(), model.Source{Path: root, Label: "t", Mode: model.ModeTree}, 0, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Status == model.StatusIncluded {
			t.Errorf("%s: в режиме tree содержимое включаться не должно", e.Rel)
		}
		// sub/loop — симлинк либо junction (тогда os.Root не разворачивает его и запись unreadable).
		loopJunction := e.Rel == "sub/loop" && e.Reason == model.ReasonUnreadable
		if e.Reason != model.ReasonModeTree && e.Reason != model.ReasonSymlink && !loopJunction {
			t.Errorf("%s: причина %q, ожидали mode-tree", e.Rel, e.Reason)
		}
	}
}

func TestWalkIgnoreAndPrune(t *testing.T) {
	root := t.TempDir()
	testkit.WriteString(t, root, "main.go", "package main\n")
	testkit.WriteString(t, root, "node_modules/a/b/c.js", "x")
	testkit.WriteString(t, root, "node_modules/d.js", "x")
	testkit.WriteString(t, root, "app.log", "x")
	testkit.WriteString(t, root, "app.min.js", "x")
	testkit.WriteString(t, root, ".gitignore", "*.log\n")
	testkit.WriteString(t, root, "sub/.gitignore", "secret.txt\n")
	testkit.WriteString(t, root, "sub/secret.txt", "x")
	testkit.WriteString(t, root, "sub/open.txt", "x")

	entries := byRel(walkFull(t, root, Limits{UseGitignore: true, Exclude: []string{"**/*.min.js"}}))
	cases := map[string]struct {
		status model.Status
		reason model.Reason
		dir    bool
	}{
		"main.go":        {model.StatusIncluded, "", false},
		"node_modules":   {model.StatusSkipped, model.ReasonDefault, true},
		"app.log":        {model.StatusSkipped, model.ReasonGitignore, false},
		"app.min.js":     {model.StatusSkipped, model.ReasonGlob, false},
		"sub/secret.txt": {model.StatusSkipped, model.ReasonGitignore, false},
		"sub/open.txt":   {model.StatusIncluded, "", false},
		".gitignore":     {model.StatusIncluded, "", false},
		"sub/.gitignore": {model.StatusIncluded, "", false},
	}
	for rel, c := range cases {
		e, ok := entries[rel]
		if !ok || e.Status != c.status || e.Reason != c.reason || e.Dir != c.dir {
			t.Errorf("%s: получили %+v, ожидали %v", rel, e, c)
		}
	}
	// Содержимое отсечённого каталога не должно появляться вовсе: обход в него не заходил.
	for rel := range entries {
		if strings.HasPrefix(rel, "node_modules/") {
			t.Errorf("обход зашёл в отсечённый каталог: %s", rel)
		}
	}
	if e := entries["node_modules"]; e.Rule != "node_modules/" {
		t.Errorf("правило отсечения = %q", e.Rule)
	}

	// Без UseGitignore правила из файлов не действуют.
	entries = byRel(walkFull(t, root, Limits{}))
	if entries["app.log"].Status != model.StatusIncluded {
		t.Error("без UseGitignore app.log должен быть включён")
	}
}

func TestWalkSymlinkOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	testkit.WriteString(t, outside, "secret.txt", "TOP SECRET")
	root := t.TempDir()
	testkit.WriteString(t, root, "ok.txt", "x")
	target, _ := filepath.Abs(filepath.Join(outside, "secret.txt"))
	if err := os.Symlink(target, filepath.Join(root, "evil.txt")); err != nil {
		t.Skipf("симлинки недоступны: %v", err)
	}
	entries := byRel(walkFull(t, root, Limits{}))
	if e := entries["evil.txt"]; e.Status == model.StatusIncluded {
		t.Errorf("симлинк за пределы корня включён: %+v", e)
	}
}

func TestWalkLimits(t *testing.T) {
	root := t.TempDir()
	for i := range 300 {
		testkit.WriteString(t, root, fmt.Sprintf("d%d/f.txt", i), "x")
	}
	_, err := Walk(context.Background(), model.Source{Path: root, Label: "t", Mode: model.ModeFull}, 0, Limits{MaxFiles: 100})
	if !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("ожидали ErrTooManyFiles, получили %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Walk(ctx, model.Source{Path: root, Label: "t", Mode: model.ModeFull}, 0, Limits{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("отмена контекста: получили %v", err)
	}

	deep := t.TempDir()
	testkit.WriteString(t, deep, "a/b/c/d/e.txt", "x")
	entries := byRel(walkFull(t, deep, Limits{MaxDepth: 2}))
	if e := entries["a/b/c"]; e.Reason != model.ReasonTooDeep || !e.Dir {
		t.Errorf("a/b/c: ожидали too-deep, получили %+v", e)
	}

	if got := ClampMaxFileSize(-5); got != model.DefaultMaxFileSize {
		t.Errorf("отрицательный лимит: %d", got)
	}
	if got := ClampMaxFileSize(1 << 40); got != HardMaxFileSize {
		t.Errorf("огромный лимит не зажат: %d", got)
	}
}

func TestWalkSourceNotDir(t *testing.T) {
	root := t.TempDir()
	testkit.WriteString(t, root, "f.txt", "x")
	_, err := Walk(context.Background(), model.Source{Path: filepath.Join(root, "f.txt"), Label: "t"}, 0, Limits{})
	if err == nil {
		t.Error("файл вместо каталога должен давать ошибку")
	}
	_, err = Walk(context.Background(), model.Source{Path: filepath.Join(root, "нет"), Label: "t"}, 0, Limits{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ожидали ErrNotExist, получили %v", err)
	}
}

func TestReadText(t *testing.T) {
	dir := t.TempDir()
	testkit.ScanTree(t, dir)
	// NUL за пределами окна распознавания: Walk его пропустит, но ReadText обязан поймать.
	testkit.WriteString(t, dir, "late-nul.txt", strings.Repeat("a", 2000)+"\x00tail")
	// То же для кодировки: невалидные байты за окном sniff ловит только полное чтение.
	testkit.Write(t, dir, "late-cp1251.txt", append([]byte(strings.Repeat("a", 2000)), 0xCF, 0xF0, 0xE8, '\n'))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	read := func(rel string) (string, error) {
		s, _, err := ReadText(root, rel, 256*1024)
		return s, err
	}
	cases := map[string]string{
		"utf16le.txt": "привет, мир\nвторая строка\n",
		"utf16be.txt": "привет, мир\n",
		"crlf.txt":    "one\ntwo\nthree\n",
		"utf8bom.txt": "с BOM\n",
		"empty.txt":   "",
	}
	for rel, want := range cases {
		got, err := read(rel)
		if err != nil || got != want {
			t.Errorf("%s: получили %q, %v; ожидали %q", rel, got, err, want)
		}
	}
	// Невалидный UTF-8 не заменяется на U+FFFD молча: файл не включается.
	for _, rel := range []string{"cp1251.txt", "late-cp1251.txt"} {
		if got, err := read(rel); !errors.Is(err, ErrNotUTF8) || got != "" {
			t.Errorf("%s: ожидали ErrNotUTF8 без содержимого, получили %q, %v", rel, got, err)
		}
	}
	// UTF-16 с порчей (обрыв на нечётном байте, непарный суррогат) тоже не включается:
	// иначе потерянный байт или U+FFFD прошли бы под видом исходника.
	odd := testkit.UTF16("привет\n", false)
	testkit.Write(t, dir, "utf16-odd.txt", odd[:len(odd)-1])
	lone := testkit.UTF16("привет", false)
	testkit.Write(t, dir, "utf16-lone.txt", append(lone, 0x3D, 0xD8, 'x', 0)) // U+D83D без пары
	loneBE := testkit.UTF16("привет", true)
	testkit.Write(t, dir, "utf16-lone-be.txt", append(loneBE, 0xDC, 0x00, 0, 'x')) // U+DC00 без старшей половины
	pair := testkit.UTF16("a\U0001F600b\n", false)
	testkit.Write(t, dir, "utf16-pair.txt", pair)
	for _, rel := range []string{"utf16-odd.txt", "utf16-lone.txt", "utf16-lone-be.txt"} {
		if got, err := read(rel); !errors.Is(err, ErrNotUTF8) || got != "" {
			t.Errorf("%s: ожидали ErrNotUTF8 без содержимого, получили %q, %v", rel, got, err)
		}
	}
	if got, err := read("utf16-pair.txt"); err != nil || got != "a\U0001F600b\n" {
		t.Errorf("utf16-pair.txt: законная пара суррогатов испорчена: %q, %v", got, err)
	}
	// Обрыв префикса на границе окна — норма: classify порчей его не считает.
	if v := classify(odd, true); v != verdictText {
		t.Errorf("classify(UTF-16 с обрывом) = %v, ожидали verdictText", v)
	}
	for _, rel := range []string{"nul.bin", "utf16nul.dat", "late-nul.txt"} {
		if _, err := read(rel); !errors.Is(err, ErrBinary) {
			t.Errorf("%s: ожидали ErrBinary, получили %v", rel, err)
		}
	}
	if _, size, err := ReadText(root, "big.txt", 1000); !errors.Is(err, ErrTooBig) || size != 1001 {
		t.Errorf("big.txt: ожидали ErrTooBig и остановку чтения на лимите, получили %v, size=%d", err, size)
	}
	if _, _, err := ReadText(root, "../escape", 100); err == nil {
		t.Error("выход за корень должен давать ошибку")
	}
}

// Файл в cp1251 (обычный случай для Битрикс-проектов) обязан попасть в отчёт
// о пропущенном с причиной, а не уйти в бандл с потерянным содержимым.
func TestWalkEncodingReport(t *testing.T) {
	root := t.TempDir()
	// «Комментарий» в cp1251, как в Битрикс-проектах.
	testkit.Write(t, root, "legacy.php", []byte("<?php\n// \xCA\xEE\xEC\xEC\xE5\xED\xF2\xE0\xF0\xE8\xE9\n$x = \"\xC7\xED\xE0\xF7\xE5\xED\xE8\xE5\";\n"))
	// Валидный UTF-8, но многобайтный символ обрезан границей окна sniff: порчей не считается.
	testkit.WriteString(t, root, "edge.txt", strings.Repeat("a", sniffSize-1)+"ж"+strings.Repeat("б", 50))
	entries := byRel(walkFull(t, root, Limits{}))
	e := entries["legacy.php"]
	if e.Status != model.StatusSkipped || e.Reason != model.ReasonEncoding || !strings.Contains(e.Rule, "не в UTF-8") {
		t.Errorf("legacy.php: %+v", e)
	}
	if e := entries["edge.txt"]; e.Status != model.StatusIncluded {
		t.Errorf("edge.txt: обрыв символа на границе окна принят за порчу: %+v", e)
	}
}

// Junction (mklink /J) — реальный вектор цикла на Windows: прав не требует, а в
// Go идёт другой веткой, чем симлинк (тип записи не ModeSymlink). os.Root
// junction не разворачивает вовсе (path escapes from parent), и это то, что
// защищает от цикла; тест закрепляет такое поведение.
func TestWalkJunctionCycle(t *testing.T) {
	root := t.TempDir()
	testkit.WriteString(t, root, "ok.txt", "x")
	testkit.WriteString(t, root, "sub/a.go", "package sub\n")
	if !testkit.Junction(t, root, ".", "sub/loop") {
		t.Skip("junction недоступен на этой ОС")
	}
	entries := walkFull(t, root, Limits{})
	rels := byRel(entries)
	if len(entries) > 10 {
		t.Fatalf("обход ушёл по junction в цикл: %d записей", len(entries))
	}
	for rel := range rels {
		if strings.HasPrefix(rel, "sub/loop/") {
			t.Errorf("обход зашёл внутрь junction: %s", rel)
		}
	}
	if e, ok := rels["sub/loop"]; !ok || e.Status == model.StatusIncluded {
		t.Errorf("sub/loop должен быть в отчёте как пропущенный: %+v, %v", e, ok)
	} else {
		t.Logf("sub/loop: %s/%s %q", e.Status, e.Reason, e.Rule)
	}
	if rels["ok.txt"].Status != model.StatusIncluded || rels["sub/a.go"].Status != model.StatusIncluded {
		t.Errorf("обычные файлы потеряны: %+v", entries)
	}
}

// Junction наружу корня: os.Root не должен выпустить, а обход не должен
// читать чужие файлы.
func TestWalkJunctionOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	testkit.WriteString(t, outside, "secret.txt", "TOP SECRET")
	root := t.TempDir()
	testkit.WriteString(t, root, "ok.txt", "x")
	if !testkit.Junction(t, root, "../"+filepath.Base(outside), "escape") {
		t.Skip("junction недоступен на этой ОС")
	}
	entries := walkFull(t, root, Limits{})
	for _, e := range entries {
		if strings.Contains(e.Rel, "secret") {
			t.Errorf("через junction прочитан файл вне корня: %+v", e)
		}
	}
	// Тихое исчезновение записи — потеря поддерева без строки в отчёте.
	if e, ok := byRel(entries)["escape"]; !ok || e.Status != model.StatusSkipped {
		t.Errorf("escape должен быть в плане как пропущенный: %+v, %v", e, ok)
	}
}
