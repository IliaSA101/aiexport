package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IliaSA101/aiexport/internal/model"
)

func TestParseSource(t *testing.T) {
	dir := t.TempDir()
	got, err := parseSource("full|Мой проект|" + dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != model.ModeFull || got.Label != "Мой проект" || got.Path != dir {
		t.Errorf("разбор: %+v", got)
	}
	// Двоеточие после буквы диска и «|» в самом пути не мешают разбору.
	got, err = parseSource(`tree|Подпись|D:\a|b\c`)
	if err != nil || got.Mode != model.ModeTree || !strings.HasSuffix(got.Path, `a|b\c`) {
		t.Errorf("путь с разделителем: %+v, %v", got, err)
	}
	for _, bad := range []string{"", "full|только-два", "skeleton|x|" + dir, "full|x|"} {
		if _, err := parseSource(bad); err == nil {
			t.Errorf("%q: ожидали ошибку", bad)
		}
	}
}

func TestExportUsageErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]string{
		"без источников":   {"export", "--out", "x.md"},
		"без out":          {"export", "--src", "full|x|" + dir},
		"плохой источник":  {"export", "--src", "oops", "--out", "x.md"},
		"негодная маска":   {"export", "--src", "full|x|" + dir, "--out", "x.md", "--exclude", "[a"},
		"неизвестный флаг": {"export", "--nope"},
		"лишний аргумент":  {"export", "--src", "full|x|" + dir, "--out", "x.md", "лишний"},
	}
	for name, args := range cases {
		var out bytes.Buffer
		err := run(context.Background(), args, &out)
		if !errors.Is(err, errUsage) {
			t.Errorf("%s: ожидали errUsage, получили %v", name, err)
		}
		if out.Len() == 0 {
			t.Errorf("%s: причина должна быть напечатана до выхода с кодом 2", name)
		}
	}
}

func TestExportEndToEnd(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(proj, "bundle.md") // нарочно внутри источника

	args := []string{"export", "--src", "full|Проект|" + proj, "--out", outPath, "--instructions", "Привет"}
	for i := range 2 { // второй запуск не должен втянуть в бандл результат первого
		if i == 1 {
			args = append(args, "--force") // файл уже есть: без --force отказ (см. TestExportOutGuards)
		}
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("export: %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "Готово: файлов 1,") {
			t.Errorf("сводка: %q", out.String())
		}
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, s := range []string{"## Манифест", "1. [Проект] main.go", "package main", "Привет", "правило: /bundle.md"} {
		if !strings.Contains(got, s) {
			t.Errorf("в бандле нет %q", s)
		}
	}
	if strings.Contains(got, "## [Проект] bundle.md") {
		t.Error("бандл включил сам себя")
	}
}

// --out не должен молча заменять существующий файл, уходить в сеть и создавать
// файлы с именами устройств Windows.
func TestExportOutGuards(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	important := filepath.Join(outDir, "important.md")
	if err := os.WriteFile(important, []byte("ВАЖНОЕ"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := func(extra ...string) (string, error) {
		var out bytes.Buffer
		err := run(context.Background(), append([]string{"export", "--src", "full|P|" + proj}, extra...), &out)
		return out.String(), err
	}

	// Существующий файл без --force: отказ, содержимое цело, код usage (2).
	msg, err := export("--out", important)
	if !errors.Is(err, errUsage) || !strings.Contains(msg, "--force") {
		t.Errorf("ожидали отказ с подсказкой про --force: %v, %q", err, msg)
	}
	if b, _ := os.ReadFile(important); string(b) != "ВАЖНОЕ" {
		t.Errorf("существующий файл изменён: %q", b)
	}
	// Каталог вместо файла отвергается даже с --force.
	if _, err := export("--out", outDir, "--force"); !errors.Is(err, errUsage) {
		t.Errorf("каталог в --out: %v", err)
	}
	// С --force заменяется.
	if _, err := export("--out", important, "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if b, _ := os.ReadFile(important); !strings.Contains(string(b), "package main") {
		t.Errorf("файл не заменён: %q", b)
	}

	if runtime.GOOS == "windows" {
		for _, bad := range []string{
			`\\host\share\b.md`, `\??\UNC\host\share\b.md`, `/\host\share\b.md`,
			filepath.Join(outDir, "con"), filepath.Join(outDir, "LPT9.md"), filepath.Join(outDir, "nul.txt"), "NUL", "C:b.md",
		} {
			if _, err := export("--out", bad); !errors.Is(err, errUsage) {
				t.Errorf("--out %q: ожидали отказ, получили %v", bad, err)
			}
		}
		if _, err := parseSource(`full|x|\??\UNC\host\share`); err == nil {
			t.Error("источник с NT-путём должен отвергаться ещё при разборе")
		}
	}
}
