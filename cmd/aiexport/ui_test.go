package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// runUITest поднимает UI на свободном порту, делает один запрос и гасит сервер.
// Настоящий браузер в тестах не открывается никогда: open подменён.
func runUITest(t *testing.T, noBrowser bool) (opened []string, page int) {
	t.Helper()
	port := freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	opens := make(chan string, 4)
	open := func(_ context.Context, url string) error { opens <- url; return nil }
	done := make(chan error, 1)
	go func() { done <- runUI(ctx, port, noBrowser, &out, open) }()

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			page = resp.StatusCode
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("UI не поднялся: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("штатная остановка вернула %v", err)
	}
	close(opens)
	for u := range opens {
		opened = append(opened, u)
	}
	if !strings.Contains(out.String(), url) {
		t.Errorf("адрес не напечатан: %q", out.String())
	}
	return opened, page
}

func TestRunUIOpensBrowserUnlessDisabled(t *testing.T) {
	opened, page := runUITest(t, false)
	if page != http.StatusOK || len(opened) != 1 {
		t.Errorf("страница %d, открытий браузера %d", page, len(opened))
	}
	opened, _ = runUITest(t, true)
	if len(opened) != 0 {
		t.Errorf("--no-browser, но браузер открыт: %v", opened)
	}
}

func TestRunUIPortBusy(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	var out bytes.Buffer
	err = runUI(t.Context(), port, true, &out, nil)
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Errorf("ожидали внятную ошибку про порт, получили %v", err)
	}
}

func TestRunBadPort(t *testing.T) {
	for _, a := range []string{"0", "70000", "-5"} {
		var out bytes.Buffer
		err := run(t.Context(), []string{"--port", a, "--no-browser"}, &out)
		if !errors.Is(err, errUsage) {
			t.Errorf("--port %s: ожидали errUsage, получили %v", a, err)
		}
	}
}
