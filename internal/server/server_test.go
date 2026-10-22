package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/IliaSA101/aiexport"
	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

const testPort = 8471

func newTestServer(t *testing.T, web fs.FS) *Server {
	t.Helper()
	counter, err := tokens.NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	if web == nil {
		web = fstest.MapFS{
			"index.html": {Data: []byte("<!doctype html><title>x</title>")},
			"app.js":     {Data: []byte("export {};")},
			"style.css":  {Data: []byte("body{}")},
			"favicon.svg": {
				Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"),
			},
		}
	}
	s, err := New(Options{Port: testPort, Counter: counter, Web: web})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// do выполняет запрос в обход сети: заголовок Host задаётся напрямую.
func do(t *testing.T, h http.Handler, method, target, host string, body any, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, target, rd)
	req.Host = host
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func okHost() string { return "127.0.0.1:" + strconv.Itoa(testPort) }

// mkTree создаёт дерево: текст, NUL-файл и node_modules.
func mkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":                 "package a\n",
		"docs/b.md":            "# заголовок\n",
		"bin.dat":              "ab\x00cd",
		"node_modules/x/i.js":  "x",
		"node_modules/x/ii.js": "y",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func specFor(root string) model.Spec {
	return model.Spec{
		Sources:      []model.Source{{Path: root, Label: "Тест", Mode: model.ModeFull}},
		UseGitignore: true,
	}
}

func TestForeignHostForbidden(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	for _, host := range []string{"evil.example", "evil.example:8471", "127.0.0.1:9999", "127.0.0.1", "[::1]:8471", ""} {
		rec := do(t, h, http.MethodGet, "/", host, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: код %d, ожидали 403", host, rec.Code)
		}
		var e errorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
			t.Errorf("Host %q: тело не JSON-ошибка: %q", host, rec.Body.String())
		}
	}
	for _, host := range []string{"127.0.0.1:8471", "localhost:8471", "LOCALHOST:8471"} {
		if rec := do(t, h, http.MethodGet, "/", host, nil); rec.Code != http.StatusOK {
			t.Errorf("Host %q: код %d, ожидали 200", host, rec.Code)
		}
	}
	// API закрыт тем же замком, а не только страница.
	if rec := do(t, h, http.MethodGet, "/api/browse?path=C:%5C", "evil.example", nil); rec.Code != http.StatusForbidden {
		t.Errorf("/api/browse с чужим Host: код %d", rec.Code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	spec := specFor(t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/plan", okHost(), spec, "Origin", "http://evil.example")
	if rec.Code != http.StatusForbidden {
		t.Errorf("чужой Origin: код %d, ожидали 403", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/plan", okHost(), spec, "Content-Type", "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: код %d, ожидали 415", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/plan", okHost(), spec, "Origin", "http://localhost:8471")
	if rec.Code != http.StatusOK {
		t.Errorf("свой Origin: код %d, тело %s", rec.Code, rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	rec := do(t, h, http.MethodGet, "/", okHost(), nil)
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("CSP = %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("нет nosniff")
	}
}

func TestStaticContentTypes(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	cases := map[string]string{
		"/":            "text/html",
		"/index.html":  "text/html", // FileServer отвечает редиректом на «/», проверяем отдельно ниже
		"/app.js":      "text/javascript",
		"/style.css":   "text/css",
		"/favicon.svg": "image/svg+xml",
		"/missing.js":  "",
	}
	for path, want := range cases {
		rec := do(t, h, http.MethodGet, path, okHost(), nil)
		switch {
		case path == "/index.html":
			if rec.Code != http.StatusMovedPermanently {
				t.Errorf("%s: код %d", path, rec.Code)
			}
		case want == "":
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: код %d, ожидали 404", path, rec.Code)
			}
		default:
			if rec.Code != http.StatusOK {
				t.Errorf("%s: код %d", path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, want) {
				t.Errorf("%s: Content-Type %q, ожидали %q", path, ct, want)
			}
		}
	}
}

// Настоящий вшитый web/: каждый модуль должен отдаваться как JavaScript.
func TestEmbeddedWebServed(t *testing.T) {
	web, err := aiexport.WebFS()
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(t, web).Handler()
	for _, name := range []string{"app.js", "state.js", "api.js", "sources.js", "picker.js", "result.js", "dom.js"} {
		rec := do(t, h, http.MethodGet, "/"+name, okHost(), nil)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
			t.Errorf("%s: код %d, Content-Type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, name := range []string{"/", "/style.css", "/favicon.svg"} {
		if rec := do(t, h, http.MethodGet, name, okHost(), nil); rec.Code != http.StatusOK {
			t.Errorf("%s: код %d", name, rec.Code)
		}
	}
}

func TestPlanTotals(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)
	rec := do(t, h, http.MethodPost, "/api/plan", okHost(), specFor(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var resp PlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Totals.Included != 2 || len(resp.Files) != 2 {
		t.Errorf("включено %d (files %d), ожидали 2: %+v", resp.Totals.Included, len(resp.Files), resp.Files)
	}
	// node_modules отсечён целиком — одна запись, а не две.
	if resp.SkipByReason["default"] != 1 || resp.SkipByReason["binary"] != 1 {
		t.Errorf("skipByReason = %v", resp.SkipByReason)
	}
	if resp.Totals.Skipped != 2 || len(resp.Skipped) != 2 {
		t.Errorf("пропущено %d (skipped %d), ожидали 2", resp.Totals.Skipped, len(resp.Skipped))
	}
	if resp.Totals.Tokens <= 0 || resp.Totals.Zone != "green" {
		t.Errorf("totals = %+v", resp.Totals)
	}
	if len(resp.Sources) != 1 || resp.Sources[0].Label != "Тест" || resp.Sources[0].Files != 2 ||
		resp.Sources[0].Tokens != resp.Totals.Tokens {
		t.Errorf("sources = %+v", resp.Sources)
	}
}

func TestPlanEmptyLabelAndErrors(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)

	spec := specFor(root)
	spec.Sources[0].Label = ""
	rec := do(t, h, http.MethodPost, "/api/plan", okHost(), spec)
	var resp PlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("пустая подпись: код %d, %v", rec.Code, err)
	}
	if resp.Sources[0].Label != filepath.Base(root) {
		t.Errorf("подпись = %q", resp.Sources[0].Label)
	}

	bad := []model.Spec{
		{},
		{Sources: []model.Source{{Path: `\\server\share`, Mode: model.ModeFull}}},
		{Sources: []model.Source{{Path: root, Mode: "skeleton"}}},
		{Sources: []model.Source{{Path: filepath.Join(root, "нет"), Mode: model.ModeFull}}},
		{Sources: []model.Source{{Path: filepath.Join(root, "a.go"), Mode: model.ModeFull}}},
	}
	for i, sp := range bad {
		rec := do(t, h, http.MethodPost, "/api/plan", okHost(), sp)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("случай %d: код %d, ожидали 400", i, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/api/plan", okHost(), nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/plan: код %d", rec.Code)
	}
}

func TestExportWritesFile(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)
	out := filepath.Join(t.TempDir(), "bundle.md")

	rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Out: out})
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var resp ExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != out || resp.Tokens <= 0 || resp.Bytes <= 0 {
		t.Errorf("ответ = %+v", resp)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != resp.Bytes || !strings.Contains(string(data), "package a") {
		t.Errorf("файл не совпадает с ответом: %d байт", len(data))
	}
}

// Имя выбирает сервер, а файл вывода внутри источника не попадает в собственный бандл.
func TestExportDefaultNameInsideSource(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)
	t.Chdir(root)

	rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root)})
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var resp ExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(resp.Path) != root || !strings.HasPrefix(filepath.Base(resp.Path), "aiexport-") {
		t.Fatalf("путь = %q", resp.Path)
	}
	data, err := os.ReadFile(resp.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "## [Тест] "+filepath.Base(resp.Path)) {
		t.Error("бандл включил сам себя")
	}
}

func TestExportDownloadAndBadOut(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)

	rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Download: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, ".md") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rec.Body.String(), "package a") {
		t.Error("в теле нет содержимого файла")
	}

	bad := []string{root, filepath.Join(root, "нет", "b.md")}
	if runtime.GOOS == "windows" {
		bad = append(bad, `\\server\share\b.md`)
	}
	for _, out := range bad {
		rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Out: out})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("out %q: код %d, ожидали 400", out, rec.Code)
		}
	}

	// На POSIX «\\server\share\b.md» — обычное имя файла с обратными слэшами, а не UNC.
	// Раньше тест передавал его относительным путём, и выгрузка ложилась в каталог пакета.
	if runtime.GOOS != "windows" {
		out := filepath.Join(t.TempDir(), `\\server\share\b.md`)
		rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Out: out})
		if rec.Code != http.StatusOK {
			t.Fatalf("out %q: код %d, ожидали 200: %s", out, rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("файл вывода не создан по указанному пути: %v", err)
		}
	}
}

func TestRunPortBusyAndShutdown(t *testing.T) {
	counter, err := tokens.NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	web := fstest.MapFS{"index.html": {Data: []byte("ok")}}
	s, err := New(Options{Port: port, Counter: counter, Web: web})
	if err != nil {
		t.Fatal(err)
	}

	// Порт занят нами же: ошибка, а не молчаливый уход на другой порт.
	if err := s.Run(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "--port") {
		t.Errorf("ожидали ошибку про занятый порт, получили %v", err)
	}
	_ = ln.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, func(u string) { ready <- u }) }()

	var url string
	select {
	case url = <-ready:
	case err := <-done:
		t.Fatalf("Run завершился раньше времени: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не поднялся")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("код %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("штатная остановка вернула %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("сервер не остановился")
	}
}

func TestNewValidation(t *testing.T) {
	counter, err := tokens.NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{}
	for _, opt := range []Options{
		{Port: 0, Counter: counter, Web: web},
		{Port: 70000, Counter: counter, Web: web},
		{Port: 8471, Web: web},
		{Port: 8471, Counter: counter},
	} {
		if _, err := New(opt); err == nil {
			t.Errorf("New(%+v) должен вернуть ошибку", opt.Port)
		}
	}
}

// Записи mode-tree не должны съедать лимит списка пропущенного: на большом
// tree-источнике настоящие пропуски иначе не попали бы в таблицу вовсе.
func TestPlanTreeOnlyNotInSkippedList(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	full := mkTree(t)
	tree := t.TempDir()
	for i := range maxSkippedInResponse + 50 {
		if err := os.WriteFile(filepath.Join(tree, fmt.Sprintf("f%04d.md", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec := specFor(full)
	spec.Sources = append(spec.Sources, model.Source{Path: tree, Label: "Дерево", Mode: model.ModeTree})
	rec := do(t, h, http.MethodPost, "/api/plan", okHost(), spec)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var resp PlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	n := maxSkippedInResponse + 50
	if resp.Totals.TreeOnly != n || resp.SkipByReason["mode-tree"] != n {
		t.Errorf("treeOnly = %d, skipByReason[mode-tree] = %d, ожидали %d", resp.Totals.TreeOnly, resp.SkipByReason["mode-tree"], n)
	}
	// Настоящие пропуски (node_modules и bin.dat) на месте и не вытеснены.
	if resp.Totals.Skipped != 2 || len(resp.Skipped) != 2 {
		t.Errorf("skipped = %d, список %d, ожидали 2", resp.Totals.Skipped, len(resp.Skipped))
	}
	for _, f := range resp.Skipped {
		if f.Reason == model.ReasonModeTree {
			t.Errorf("mode-tree в списке пропущенного: %s", f.Rel)
		}
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"treeOnly"`)) {
		t.Error("в JSON нет поля treeOnly")
	}
}

// Существующий файл без force не заменяется: 409 с машинным code, содержимое цело;
// с force: true — заменяется.
func TestExportExistingFileNeedsForce(t *testing.T) {
	h := newTestServer(t, nil).Handler()
	root := mkTree(t)
	out := filepath.Join(t.TempDir(), "bundle.md")
	if err := os.WriteFile(out, []byte("важное"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Out: out})
	if rec.Code != http.StatusConflict {
		t.Fatalf("без force: код %d, ожидали 409: %s", rec.Code, rec.Body.String())
	}
	var e errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != "file-exists" || e.Error == "" {
		t.Errorf("тело отказа = %+v", e)
	}
	if data, _ := os.ReadFile(out); string(data) != "важное" {
		t.Errorf("файл тронут при отказе: %q", data)
	}

	rec = do(t, h, http.MethodPost, "/api/export", okHost(), ExportRequest{Spec: specFor(root), Out: out, Force: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("с force: код %d: %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(out); !strings.Contains(string(data), "package a") {
		t.Errorf("файл не заменён: %q", data)
	}
}
