package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func browse(t *testing.T, h http.Handler, path string) (int, BrowseResponse, errorResponse) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/browse?path="+url.QueryEscape(path), okHost(), nil)
	var ok BrowseResponse
	var bad errorResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
			t.Fatal(err)
		}
	} else if err := json.Unmarshal(rec.Body.Bytes(), &bad); err != nil {
		t.Fatalf("тело ошибки не JSON: %q", rec.Body.String())
	}
	return rec.Code, ok, bad
}

func TestBrowseRejectsServicePaths(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	for _, p := range []string{
		`\\server\share`,
		`\\server\share\dir`,
		`\\?\C:\`,
		`\\.\C:\`,
		`\??\C:\`,
		`//server/share`,
		`/\server\share`,
		`\/server/share`,
		`  \\server\share`,
	} {
		code, _, e := browse(t, h, p)
		if code != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%q: код %d, ошибка %q; ожидали 400 с текстом", p, code, e.Error)
		}
	}
}

func TestBrowseRelativeRejected(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	if code, _, _ := browse(t, h, "относительный/путь"); code != http.StatusBadRequest {
		t.Errorf("код %d", code)
	}
}

func TestBrowseListsFullPathsSkippingHidden(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := t.TempDir()
	for _, d := range []string{"beta", "Alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, resp, _ := browse(t, h, root)
	if code != http.StatusOK {
		t.Fatalf("код %d", code)
	}
	want := []string{filepath.Join(root, "Alpha"), filepath.Join(root, "beta")}
	if len(resp.Dirs) != 2 || resp.Dirs[0] != want[0] || resp.Dirs[1] != want[1] {
		t.Errorf("dirs = %v, ожидали %v", resp.Dirs, want)
	}
	if resp.Path != root || resp.Parent != filepath.Dir(root) {
		t.Errorf("path/parent = %q / %q", resp.Path, resp.Parent)
	}

	if code, _, _ := browse(t, h, filepath.Join(root, "file.txt")); code != http.StatusBadRequest {
		t.Errorf("файл вместо папки: код %d", code)
	}
	if code, _, _ := browse(t, h, filepath.Join(root, "нет")); code != http.StatusNotFound {
		t.Errorf("несуществующая папка: код %d", code)
	}
}

func TestBrowseRootAndDrives(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	if runtime.GOOS != "windows" {
		t.Skip("диски есть только в Windows")
	}
	code, resp, _ := browse(t, h, "")
	if code != http.StatusOK || len(resp.Drives) == 0 || resp.Path != "" || resp.Parent != "" {
		t.Fatalf("список дисков: код %d, %+v", code, resp)
	}
	if resp.Drives[0][len(resp.Drives[0])-2:] != `:\` {
		t.Errorf("диск %q не вида C:\\", resp.Drives[0])
	}
	drive := resp.Drives[0]
	// На корне диска parent пуст.
	code, resp, _ = browse(t, h, drive)
	if code != http.StatusOK || resp.Parent != "" || resp.Path != drive {
		t.Errorf("корень диска: код %d, %+v", code, resp)
	}
}
