package bundle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/plan"
	"github.com/IliaSA101/aiexport/internal/testkit"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

func buildPlan(t *testing.T, spec model.Spec) model.Plan {
	t.Helper()
	c, err := tokens.NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(context.Background(), c, spec)
	if err != nil {
		t.Fatalf("plan.Build: %v", err)
	}
	p.Created = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	return p
}

func goldenSpec(t *testing.T) model.Spec {
	t.Helper()
	proj, schemes := t.TempDir(), t.TempDir()
	testkit.WriteString(t, proj, "main.go", "package main\n\nfunc main() {}\n")
	testkit.WriteString(t, proj, "README.md", "# Проект\r\nописание\r\n")
	testkit.Write(t, proj, "notes/utf16.txt", testkit.UTF16("заметка в UTF-16\n", false))
	testkit.WriteString(t, proj, "docs/fences.md", "Пример:\n```go\nx := 1\n```\n")
	testkit.WriteString(t, proj, "empty.txt", "")
	testkit.WriteString(t, proj, "app.log", "ЛОГ-ТЕКСТ-УТЕЧКА")
	testkit.WriteString(t, proj, ".gitignore", "*.log\n")
	testkit.WriteString(t, proj, "node_modules/lib/index.js", "x")
	testkit.Write(t, proj, "blob.bin", []byte("MZ\x00\x00"))
	testkit.WriteString(t, schemes, "diagram.puml", "@startuml\n@enduml\n")
	testkit.WriteString(t, schemes, "docs/a.md", "# A\n")
	return model.Spec{
		Sources: []model.Source{
			{Path: proj, Label: "Проект", Mode: model.ModeFull},
			{Path: schemes, Label: "Схемы", Mode: model.ModeTree},
		},
		UseGitignore: true,
		Instructions: "Ответь кратко.",
	}
}

func render(t *testing.T, p model.Plan) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(context.Background(), p, &buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

// Golden-файл. Обновление: UPDATE_GOLDEN=1 go test ./internal/bundle -run Golden
// (переменная окружения, а не флаг: флаг потребовал бы переменной уровня пакета).
func TestRenderGolden(t *testing.T) {
	got := render(t, buildPlan(t, goldenSpec(t)))
	golden := filepath.Join("testdata", "golden.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("нет golden-файла (UPDATE_GOLDEN=1 создаст): %v", err)
	}
	if string(want) != got {
		t.Errorf("бандл не совпал с golden-файлом.\n--- получили ---\n%s\n--- ожидали ---\n%s", got, want)
	}
}

func TestRenderSections(t *testing.T) {
	got := render(t, buildPlan(t, goldenSpec(t)))
	for _, s := range []string{
		"## Манифест", "## Дерево", "## Что сознательно не включено", "## Файлы",
		"## Источники только деревом", "## Инструкции", "Ответь кратко.",
		"[Проект] main.go", "режим tree: содержимое не включено намеренно",
		"заметка в UTF-16", // UTF-16 декодирован
	} {
		if !strings.Contains(got, s) {
			t.Errorf("в бандле нет %q", s)
		}
	}
	// Содержимое tree-источника и исключённого не должно просочиться.
	for _, s := range []string{"@startuml", "ЛОГ-ТЕКСТ-УТЕЧКА", "node_modules/lib/index.js"} {
		if strings.Contains(got, s) {
			t.Errorf("в бандле лишнее %q", s)
		}
	}
	if strings.Contains(got, "\r") {
		t.Error("CRLF должен нормализоваться в LF")
	}
}

// Файл с тройным апострофом внутри не должен рвать бандл: фенс делается длиннее.
func TestRenderLongerFence(t *testing.T) {
	got := render(t, buildPlan(t, goldenSpec(t)))
	if !strings.Contains(got, "````markdown\nПример:\n```go\nx := 1\n```\n````\n") {
		t.Errorf("ожидали четырёхапостофный фенс вокруг fences.md:\n%s", got)
	}
	if n := longestBacktickRun("a ``` b `````` c"); n != 6 {
		t.Errorf("longestBacktickRun = %d", n)
	}
}

// Файл изменился между планом и сборкой: пометка в бандле, а не тишина.
func TestRenderSizeMismatch(t *testing.T) {
	spec := goldenSpec(t)
	p := buildPlan(t, spec)
	testkit.WriteString(t, spec.Sources[0].Path, "main.go", "package main\n\n// дописано после плана\nfunc main() {}\n")
	got := render(t, p)
	if !strings.Contains(got, "ВНИМАНИЕ: размер файла изменился") {
		t.Error("нет пометки об изменении файла")
	}
	if !strings.Contains(got, "дописано после плана") {
		t.Error("содержимое должно быть актуальным на момент сборки")
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(b []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("диск полон")
	}
	f.after -= len(b)
	return len(b), nil
}

func TestRenderWriteError(t *testing.T) {
	p := buildPlan(t, goldenSpec(t))
	if err := Render(context.Background(), p, &failWriter{}); err == nil {
		t.Error("ошибка записи должна возвращаться")
	}
}

func TestRenderCancelled(t *testing.T) {
	p := buildPlan(t, goldenSpec(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	if err := Render(ctx, p, &buf); !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, получили %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := buildPlan(t, goldenSpec(t))
	dir := t.TempDir()
	target := filepath.Join(dir, "bundle.md")
	if err := os.WriteFile(target, []byte("старый бандл"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Сбой: старый файл цел, временных файлов не осталось.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteFile(ctx, p, target); err == nil {
		t.Fatal("ожидали ошибку при отменённом контексте")
	}
	if b, _ := os.ReadFile(target); string(b) != "старый бандл" {
		t.Errorf("старый файл испорчен: %q", b)
	}
	assertNoTmp(t, dir)

	// Успех: файл заменён, мусора нет.
	if err := WriteFile(context.Background(), p, target); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	b, err := os.ReadFile(target)
	if err != nil || !strings.HasPrefix(string(b), "# Бандл aiexport") {
		t.Errorf("содержимое: %v %q", err, b[:min(len(b), 40)])
	}
	assertNoTmp(t, dir)

	// Каталога назначения нет: внятная ошибка.
	if err := WriteFile(context.Background(), p, filepath.Join(dir, "нет", "b.md")); err == nil {
		t.Error("ожидали ошибку для несуществующего каталога")
	}
}

func assertNoTmp(t *testing.T, dir string) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) != 0 {
		t.Errorf("остались временные файлы: %v", left)
	}
}

// Отменённый контекст на плане без включённых файлов тоже обязан прерывать
// рендер: иначе после Ctrl+C записался бы файл.
func TestRenderCancelledWithoutIncluded(t *testing.T) {
	dir := t.TempDir()
	testkit.WriteString(t, dir, "a.txt", "x")
	p := buildPlan(t, model.Spec{Sources: []model.Source{{Path: dir, Mode: model.ModeTree}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	if err := Render(ctx, p, &buf); !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, получили %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.md")
	if err := WriteFile(ctx, p, out); !errors.Is(err, context.Canceled) {
		t.Errorf("WriteFile: ожидали context.Canceled, получили %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("файл не должен быть создан после отмены")
	}
}
