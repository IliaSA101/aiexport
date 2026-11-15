// Команда aiexport — кроссплатформенный упаковщик кода для ИИ.
// Режимы: веб-UI (без аргументов), подкоманда export (без браузера), --version.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/IliaSA101/aiexport/internal/server"
)

// version подставляется при сборке: -ldflags "-X main.version=0.1.0".
// Отдельного файла с версией нет сознательно: единственный источник — флаг сборки,
// поэтому рассинхрон «в файле одно, в бинарнике другое» невозможен по построению.
var version string

// errUsage — признак «аргументы разобрать не удалось». Пакет flag уже напечатал
// и причину, и справку, поэтому main по этому признаку молча выходит с кодом 2,
// а не печатает то же самое второй раз.
//
// Это неизменяемое значение уровня пакета — так в Go принято объявлять
// «опознаваемые» ошибки. Правило «никаких глобальных переменных» про изменяемое
// состояние, а не про такие константы-ошибки.
var errUsage = errors.New("неверные аргументы")

func main() {
	// Лог идёт в stderr, чтобы stdout остался чистым: в headless-режиме туда
	// может писаться сам бандл, и строки лога его испортили бы.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Ctrl+C отменяет контекст, а не убивает процесс на месте: обход дерева и
	// HTTP-сервер должны завершиться сами, не бросив файл записанным наполовину.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// Про ошибки в аргументах flag уже напечатал сам — второй раз не повторяем.
		// Код 2 для неверных аргументов — обычная конвенция командных утилит.
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		slog.Error("завершение с ошибкой", "err", err)
		os.Exit(1)
	}
}

// run вынесен из main, чтобы его можно было вызвать из теста: аргументы и место
// вывода приходят параметрами, os.Exit внутри нет.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	// Подкоманды разбирают свои флаги сами, поэтому ветвление — до общего FlagSet.
	if len(args) > 0 && args[0] == "export" {
		return runExport(ctx, args[1:], stdout)
	}

	fs := flag.NewFlagSet("aiexport", flag.ContinueOnError)
	fs.SetOutput(stdout)
	showVersion := fs.Bool("version", false, "показать версию и выйти")
	port := fs.Int("port", server.DefaultPort, "порт веб-UI на 127.0.0.1")
	noBrowser := fs.Bool("no-browser", false, "не открывать браузер (для автоматических прогонов)")

	if err := fs.Parse(args); err != nil {
		// Два %w: вызывающий может опознать и errUsage, и flag.ErrHelp (для -h).
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	if *showVersion {
		return printLine(stdout, "aiexport "+buildVersion())
	}
	if fs.NArg() > 0 {
		return usageErr(fs, fmt.Errorf("лишние аргументы: %s", strings.Join(fs.Args(), " ")))
	}
	if *port < 1 || *port > 65535 {
		return usageErr(fs, fmt.Errorf("порт %d вне диапазона 1..65535", *port))
	}

	slog.Info("запуск", "версия", buildVersion())
	return runUI(ctx, *port, *noBrowser, stdout, openBrowser)
}

// printLine пишет строку в вывод. Ошибку записи приходится обрабатывать — stdout
// может быть закрыт или перенаправлен в полный диск, и молча терять это нельзя.
func printLine(w io.Writer, s string) error {
	if _, err := fmt.Fprintln(w, s); err != nil {
		return fmt.Errorf("запись в вывод: %w", err)
	}
	return nil
}

// buildVersion отдаёт версию: из флага сборки, иначе из метаданных модуля
// (их записывает сам go), иначе признаётся, что это сборка из рабочей копии.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(dev)"
}
