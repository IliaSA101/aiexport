package bundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IliaSA101/aiexport/internal/model"
)

// WriteFile рендерит бандл в файл атомарно: сначала временный файл рядом,
// Sync, Close, потом Rename. Без Sync после сбоя питания на месте бандла остаётся
// пустой файл; без временного файла сбой посреди записи оставил бы обрезанный
// бандл поверх хорошего. При любой ошибке временный файл удаляется.
func WriteFile(ctx context.Context, p model.Plan, path string) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("создание временного файла в %s: %w", dir, err)
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if err != nil {
			if !closed {
				_ = f.Close()
			}
			_ = os.Remove(tmp)
		}
	}()

	if err = Render(ctx, p, f); err != nil {
		return err
	}
	// CreateTemp делает файл доступным только владельцу; бандл должен читаться
	// другими программами (редактор, браузер).
	if err = f.Chmod(0o644); err != nil {
		return fmt.Errorf("права на %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	closed = true
	if err = f.Close(); err != nil {
		return fmt.Errorf("закрытие %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("переименование в %s: %w", path, err)
	}
	return nil
}
