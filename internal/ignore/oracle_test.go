package ignore

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/aiexport/internal/testkit"
)

// Оракул: git — эталонная реализация. Вердикт берём из
// `git ls-files --others --exclude-standard`, то есть из итогового списка
// неисключённых файлов. Код выхода `git check-ignore` вердиктом не считаем: при
// отрицаниях он печатает последнее совпавшее правило, а не итог (проверено 07.10.2026).
func TestOracleAgainstGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git не найден в PATH: оракул-тест пропущен")
	}
	root := t.TempDir()
	all := testkit.IgnoreTree(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	git := func(args ...string) []byte {
		t.Helper()
		// Глобальный и системный конфиги отключены: чужой core.excludesFile
		// искажал бы эталон.
		cmd := exec.CommandContext(ctx, gitPath, append([]string{"-C", root, "-c", "core.ignorecase=" + boolStr(runtime.GOOS == "windows")}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
		}
		return out
	}
	git("init", "-q")
	want := splitZ(git("ls-files", "--others", "--exclude-standard", "-z"))

	// Наш вердикт: обход с отсечением каталогов, .gitignore читается при входе в каталог.
	m := newMatcher(nil, runtime.GOOS == "windows", false)
	var got []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return fs.SkipDir
			}
			if rel != "." {
				if ign, _, _ := m.MatchDir(rel); ign {
					return fs.SkipDir
				}
			}
			dirRel := rel
			if rel == "." {
				dirRel = ""
			}
			if f, oerr := os.Open(filepath.Join(p, ".gitignore")); oerr == nil {
				defer f.Close()
				if aerr := m.AddGitignore(dirRel, f); aerr != nil {
					return aerr
				}
			}
			return nil
		}
		if ign, _, _ := m.MatchFile(rel); !ign {
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход: %v", err)
	}

	slices.Sort(got)
	slices.Sort(want)
	// Защита от пустышки: эталон обязан и оставлять файлы, и исключать их.
	if len(want) < 10 || len(want) >= len(all) {
		t.Fatalf("эталон git выглядит вырожденно: %d из %d файлов", len(want), len(all))
	}
	if !slices.Equal(got, want) {
		t.Errorf("расхождение с git (всего файлов в дереве %d)\nтолько у нас: %v\nтолько у git: %v",
			len(all), diff(got, want), diff(want, got))
	}

	// Полный Match (с разбором предков) на каждом файле дерева обязан дать тот же
	// вердикт, что и обход с отсечением: две реализации одной семантики не должны
	// разойтись.
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	for _, rel := range all {
		if ign, _, _ := m.Match(rel, false); ign == wantSet[rel] {
			t.Errorf("Match(%q): ignored=%v, а git считает ignored=%v", rel, ign, !wantSet[rel])
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func splitZ(b []byte) []string {
	var out []string
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func diff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
