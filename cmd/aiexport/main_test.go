package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

// Проверяем run, а не main: main вызывает os.Exit и из теста непригоден.
// Ровно для этого run и вынесен отдельной функцией.
func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &out); err != nil {
		t.Fatalf("run вернул ошибку: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "aiexport ") {
		t.Errorf("ожидали строку вида «aiexport <версия>», получили %q", got)
	}
	if strings.Contains(got, "каркас") {
		t.Errorf("--version должен печатать только версию, получили %q", got)
	}
}

// Неверные аргументы должны опознаваться по errUsage: main по этому признаку
// выходит с кодом 2 и не печатает сообщение второй раз после пакета flag.
func TestRunUnknownFlag(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"--такого-флага-нет"}, &out)
	if !errors.Is(err, errUsage) {
		t.Errorf("ожидали errUsage, получили %v", err)
	}
}

// -h — тоже ошибка разбора аргументов, но дополнительно опознаётся как flag.ErrHelp.
func TestRunHelp(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"-h"}, &out)
	if !errors.Is(err, errUsage) {
		t.Errorf("ожидали errUsage, получили %v", err)
	}
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("ожидали flag.ErrHelp в цепочке, получили %v", err)
	}
	if !strings.Contains(out.String(), "-version") {
		t.Errorf("справка должна перечислять флаги, получили %q", out.String())
	}
}

// Прерывание, пришедшее до начала работы, не должно выглядеть успешным завершением.
func TestRunCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := run(ctx, nil, &out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, получили %v", err)
	}
}

func TestBuildVersionFallback(t *testing.T) {
	if buildVersion() == "" {
		t.Error("версия не должна быть пустой: нужен хотя бы запасной вариант")
	}
}
