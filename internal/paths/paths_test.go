package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Обходы чёрного списка, найденные аудитом: каждый из них после Clean становится
// настоящим UNC или объектным путём NT.
func TestCleanRejectsWindowsBypasses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("правила Windows")
	}
	service := []string{
		`\\server\share`, `\\server\share\dir`, `//server/share`, `\\?\C:\x`, `\\.\C:\`,
		`\??\C:\x`, `\??\UNC\h\s`, `/\h\s`, `\/h\s`, `/\?\C:\x`, `\\?\UNC\h\s`,
		`  \\server\share`, `\??\\C:\x`, `//?/C:/x`,
	}
	for _, raw := range service {
		for _, rel := range []bool{false, true} {
			if got, err := Clean(raw, rel); err == nil {
				t.Errorf("Clean(%q, %v) = %q, ожидали отказ", raw, rel, got)
			}
		}
	}
	notAbs := []string{`C:foo`, `\foo`, `nul`, `rel\dir`, `con`, `C:`}
	for _, raw := range notAbs {
		if got, err := Clean(raw, false); !errors.Is(err, ErrNotAbsolute) {
			t.Errorf("Clean(%q, false) = %q, %v; ожидали ErrNotAbsolute", raw, got, err)
		}
	}
	// Диск без каталога и корень без диска не принимаются и для файла вывода.
	for _, raw := range []string{`C:foo`, `\foo`} {
		if got, err := Clean(raw, true); err == nil {
			t.Errorf("Clean(%q, true) = %q, ожидали отказ", raw, got)
		}
	}
	if _, err := Clean(`C:\a\b:stream`, false); err == nil {
		t.Error("альтернативный поток данных должен отвергаться")
	}
}

func TestCleanReservedNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("правила Windows")
	}
	for _, raw := range []string{
		`C:\x\con`, `C:\x\CON.md`, `C:\x\LPT9.md`, `C:\x\nul.tar.gz`, `C:\x\com1`, `C:\x\aux\f.md`,
		`C:\x\prn .txt`, `C:\x\COM¹`,
	} {
		if got, err := Clean(raw, false); !errors.Is(err, ErrReserved) {
			t.Errorf("Clean(%q) = %q, %v; ожидали ErrReserved", raw, got, err)
		}
	}
	// Относительное зарезервированное имя ловится уже после Abs.
	for _, raw := range []string{`con`, `nul`, `sub\LPT9.md`} {
		if got, err := Clean(raw, true); !errors.Is(err, ErrReserved) {
			t.Errorf("Clean(%q, true) = %q, %v; ожидали ErrReserved", raw, got, err)
		}
	}
	for _, raw := range []string{`C:\x\console.md`, `C:\x\com10`, `C:\x\lpt0`, `C:\x\connect.go`, `C:\x\a.nul`, `C:\x\co`} {
		if _, err := Clean(raw, false); err != nil {
			t.Errorf("Clean(%q): %v", raw, err)
		}
	}
}

func TestCleanAcceptsNormal(t *testing.T) {
	dir := t.TempDir()
	got, err := Clean(dir, false)
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("Clean(%q) = %q, %v", dir, got, err)
	}
	if runtime.GOOS == "windows" {
		// Прямые слэши и избыточные элементы приводятся к канонической форме.
		fwd := strings.ReplaceAll(dir, `\`, "/") + "/a/../b/"
		want := filepath.Join(dir, "b")
		if got, err := Clean(fwd, false); err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; ожидали %q", fwd, got, err, want)
		}
	}
	cwd, _ := os.Getwd()
	rel, err := Clean("out.md", true)
	if err != nil || rel != filepath.Join(cwd, "out.md") {
		t.Errorf("относительный путь: %q, %v", rel, err)
	}
	for _, raw := range []string{"", "   ", "a\x00b"} {
		if _, err := Clean(raw, true); err == nil {
			t.Errorf("Clean(%q): ожидали ошибку", raw)
		}
	}
	if _, err := Clean("rel/dir", false); !errors.Is(err, ErrNotAbsolute) {
		t.Errorf("относительный путь без relativeOK: %v", err)
	}
}
