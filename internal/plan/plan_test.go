package plan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/testkit"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

func counter(t *testing.T) *tokens.Counter {
	t.Helper()
	c, err := tokens.NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuild(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	testkit.ScanTree(t, a)
	testkit.WriteString(t, a, "node_modules/x.js", "x")
	// NUL глубже окна распознавания: обход пропустит, подсчёт токенов обязан поймать.
	testkit.WriteString(t, a, "late-nul.txt", strings.Repeat("a", 2000)+"\x00")
	testkit.WriteString(t, b, "doc.md", "# Заголовок\n")

	spec := model.Spec{Sources: []model.Source{
		{Path: a, Label: "Полный", Mode: model.ModeFull},
		{Path: b, Mode: model.ModeTree},
	}}
	p, err := Build(context.Background(), counter(t), spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Spec.MaxFileSize != model.DefaultMaxFileSize || p.Spec.Sources[1].Label == "" {
		t.Errorf("Spec не нормализован: %+v", p.Spec)
	}
	byKey := map[string]model.FileEntry{}
	for _, f := range p.Files {
		byKey[fmt.Sprintf("%d:%s", f.Source, f.Rel)] = f
	}
	if f := byKey["0:ok.txt"]; f.Status != model.StatusIncluded || f.Tokens == 0 {
		t.Errorf("ok.txt: %+v", f)
	}
	if f := byKey["0:late-nul.txt"]; f.Reason != model.ReasonBinary || f.Tokens != 0 {
		t.Errorf("late-nul.txt должен быть понижен до binary: %+v", f)
	}
	if f := byKey["1:doc.md"]; f.Reason != model.ReasonModeTree {
		t.Errorf("doc.md в tree-источнике: %+v", f)
	}
	if f := byKey["0:node_modules"]; !f.Dir || f.Reason != model.ReasonDefault {
		t.Errorf("node_modules: %+v", f)
	}
	var inc, tok, tree int
	for _, f := range p.Files {
		if f.Status == model.StatusIncluded {
			inc++
			tok += f.Tokens
		}
		if f.Reason == model.ReasonModeTree {
			tree++
		}
	}
	// treeOnly — отдельный счётчик: файлы источника в режиме tree в skipped не входят.
	if tree != 1 || p.Totals.TreeOnly != tree {
		t.Errorf("treeOnly = %d, ожидали %d (ровно doc.md)", p.Totals.TreeOnly, tree)
	}
	if p.Totals.Included != inc || p.Totals.Tokens != tok || p.Totals.Skipped != len(p.Files)-inc-tree || p.Totals.Zone != tokens.ZoneGreen {
		t.Errorf("итоги не сходятся: %+v (включено %d, токенов %d)", p.Totals, inc, tok)
	}
	if p.Created.IsZero() {
		t.Error("Created не проставлен")
	}
}

func TestNormalizeErrors(t *testing.T) {
	dir := t.TempDir()
	ok := model.Source{Path: dir, Label: "x", Mode: model.ModeFull}
	cases := map[string]model.Spec{
		"нет источников":      {},
		"неизвестный режим":   {Sources: []model.Source{{Path: dir, Mode: "skeleton"}}},
		"пустой путь":         {Sources: []model.Source{{Mode: model.ModeFull}}},
		"относительный путь":  {Sources: []model.Source{{Path: "rel/dir", Mode: model.ModeFull}}},
		"UNC":                 {Sources: []model.Source{{Path: `\\server\share`, Mode: model.ModeFull}}},
		"устройство":          {Sources: []model.Source{{Path: `\\?\C:\x`, Mode: model.ModeFull}}},
		"негодная маска":      {Sources: []model.Source{ok}, Exclude: []string{"[abc"}},
		"слишком много масок": {Sources: []model.Source{ok}, Exclude: make([]string, maxExclude+1)},
	}
	// Обходы старого чёрного списка: после Clean они становились UNC или путём NT.
	if runtime.GOOS == "windows" {
		for _, raw := range []string{`\??\C:\x`, `\??\UNC\h\s`, `/\h\s`, `\/h\s`, `/\?\C:\x`, `C:foo`, `nul`, `\\.\C:\`} {
			cases["обход: "+raw] = model.Spec{Sources: []model.Source{{Path: raw, Mode: model.ModeFull}}}
		}
	}
	for name, spec := range cases {
		if _, err := Normalize(spec); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
	}
	spec, err := Normalize(model.Spec{Sources: []model.Source{ok}, MaxFileSize: 1 << 40})
	if err != nil || spec.MaxFileSize > 16<<20 {
		t.Errorf("лимит не зажат: %v, %d", err, spec.MaxFileSize)
	}
}

func TestBuildCancelled(t *testing.T) {
	dir := t.TempDir()
	for i := range 200 {
		testkit.WriteString(t, dir, fmt.Sprintf("f%d.txt", i), "hello")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, counter(t), model.Spec{Sources: []model.Source{{Path: dir, Mode: model.ModeFull}}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, получили %v", err)
	}
}

func TestBudget(t *testing.T) {
	b := newBudget(100)
	ctx := context.Background()
	n1, err := b.acquire(ctx, 80)
	if err != nil || n1 != 80 {
		t.Fatalf("acquire: %d, %v", n1, err)
	}
	// Запрос сверх остатка блокируется, пока не освободят место.
	got := make(chan int64, 1)
	go func() {
		n, _ := b.acquire(ctx, 50)
		got <- n
	}()
	select {
	case <-got:
		t.Fatal("acquire не должен проходить при нехватке бюджета")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(n1)
	select {
	case n := <-got:
		if n != 50 {
			t.Errorf("получили %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("acquire не проснулся после release")
	}
	// Запрос больше всего бюджета урезается, а не висит вечно.
	b.release(50)
	if n, err := b.acquire(ctx, 1000); err != nil || n != 100 {
		t.Errorf("огромный запрос: %d, %v", n, err)
	}
	// Отмена будит ожидающего.
	cctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := b.acquire(cctx, 1); !errors.Is(err, context.Canceled) {
			t.Errorf("ожидали отмену, получили %v", err)
		}
	})
	cancel()
	wg.Wait()
}

// Источник только деревом не порождает воркеров, поэтому отмену нельзя ловить
// только в них: Ctrl+C до сборки должен давать ошибку, а не успешный план.
func TestBuildCancelledTreeOnly(t *testing.T) {
	dir := t.TempDir()
	testkit.WriteString(t, dir, "a.txt", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, counter(t), model.Spec{Sources: []model.Source{{Path: dir, Mode: model.ModeTree}}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, получили %v", err)
	}
}

// Невалидный UTF-8 за окном распознавания обход не видит: файл обязан попасть в
// пропущенные с причиной encoding при полном чтении, а не уйти в бандл с U+FFFD.
func TestBuildLateNotUTF8(t *testing.T) {
	dir := t.TempDir()
	testkit.Write(t, dir, "legacy.php", append([]byte(strings.Repeat("a", 2000)), 0xCF, 0xF0, 0xE8, '\n'))
	p, err := Build(context.Background(), counter(t), model.Spec{Sources: []model.Source{{Path: dir, Mode: model.ModeFull}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 || p.Files[0].Status != model.StatusSkipped || p.Files[0].Reason != model.ReasonEncoding {
		t.Errorf("ожидали пропуск с причиной encoding: %+v", p.Files)
	}
	if p.Totals.Included != 0 || p.Totals.Skipped != 1 {
		t.Errorf("итоги: %+v", p.Totals)
	}
}

// Источник внутри каталога .git отвергается: правило .git считается от корня
// источника и config с токеном в url иначе ушёл бы в выгрузку.
func TestNormalizeRejectsGitDirSource(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{filepath.Join(root, ".git"), filepath.Join(root, ".GIT", "hooks")} {
		_, err := Normalize(model.Spec{Sources: []model.Source{{Path: p, Mode: model.ModeFull}}})
		if err == nil || !strings.Contains(err.Error(), ".git") {
			t.Errorf("%s: ожидали отказ про .git, получили %v", p, err)
		}
	}
	// Похожие имена не задеваем.
	for _, p := range []string{filepath.Join(root, ".github"), filepath.Join(root, "my.git")} {
		if _, err := Normalize(model.Spec{Sources: []model.Source{{Path: p, Mode: model.ModeFull}}}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
