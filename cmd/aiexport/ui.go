package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"

	"github.com/IliaSA101/aiexport"
	"github.com/IliaSA101/aiexport/internal/server"
	"github.com/IliaSA101/aiexport/internal/tokens"
)

// runUI поднимает локальный веб-UI и работает до отмены ctx.
// open вынесен параметром, чтобы тесты не открывали настоящий браузер.
func runUI(ctx context.Context, port int, noBrowser bool, stdout io.Writer, open func(context.Context, string) error) error {
	// Прерывание до начала работы не должно выглядеть успешным завершением.
	if err := ctx.Err(); err != nil {
		return err
	}
	web, err := aiexport.WebFS()
	if err != nil {
		return err
	}
	// Словарь грузится 0,3–0,8 с: один раз на процесс, а не на каждый запрос.
	counter, err := tokens.NewCounter()
	if err != nil {
		return err
	}
	srv, err := server.New(server.Options{Port: port, Counter: counter, Web: web, Logger: slog.Default()})
	if err != nil {
		return err
	}
	return srv.Run(ctx, func(url string) {
		_ = printLine(stdout, "aiexport "+buildVersion()+": интерфейс на "+url)
		if noBrowser {
			return
		}
		if err := open(ctx, url); err != nil {
			// Не фатально: адрес уже напечатан, пользователь откроет сам.
			slog.Warn("браузер не открылся, откройте адрес вручную", "err", err)
		}
	})
}

// openBrowser запускает браузер по умолчанию. Команда и адрес передаются отдельными
// аргументами, без shell: адрес собран нами, но привычка не склеивать строку для
// оболочки должна быть безусловной.
func openBrowser(ctx context.Context, url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // G204: имя программы фиксировано, url собран сервером
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url) //nolint:gosec // G204: имя программы фиксировано, url собран сервером
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", url) //nolint:gosec // G204: имя программы фиксировано, url собран сервером
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск браузера: %w", err)
	}
	// Wait нужен, чтобы не оставлять зомби-процесс; результат запуска нам не важен.
	go func() { _ = cmd.Wait() }()
	return nil
}
