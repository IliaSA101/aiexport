package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// maxBrowseDirs — потолок числа каталогов в ответе: на каталоге с сотнями тысяч
// записей UI всё равно неюзабелен, а ответ раздулся бы без пользы.
const maxBrowseDirs = 5000

// BrowseResponse — ответ /api/browse. Dirs — полные пути.
type BrowseResponse struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"` // пусто на корне диска: UI по этому показывает список дисков
	Dirs   []string `json:"dirs"`
	Drives []string `json:"drives"` // непусто, когда path пуст (Windows)
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		if runtime.GOOS == "windows" {
			writeJSON(w, http.StatusOK, BrowseResponse{Dirs: []string{}, Drives: listDrives()})
			return
		}
		raw = string(filepath.Separator)
	}
	// Нормализация и отказ на UNC — до первого обращения к ФС.
	p, err := cleanLocalPath(raw, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := os.Stat(p) //nolint:gosec // G703: путь прошёл cleanLocalPath, UNC и служебные префиксы отвергнуты
	switch {
	case err != nil:
		writeError(w, http.StatusNotFound, "папка не найдена или недоступна")
		return
	case !st.IsDir():
		writeError(w, http.StatusBadRequest, "это не папка")
		return
	}
	dirs, err := listDirs(r.Context(), p)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusForbidden, "папка недоступна для чтения")
		return
	}
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, http.StatusOK, BrowseResponse{Path: p, Parent: parent, Dirs: dirs, Drives: []string{}})
}

// listDirs возвращает полные пути подкаталогов. Скрытые и нечитаемые пропускаются:
// системные каталоги Windows не должны ронять обзор целиком.
func listDirs(ctx context.Context, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("чтение каталога: %w", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("обзор прерван: %w", err)
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || isHidden(e) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if !readable(full) {
			continue
		}
		dirs = append(dirs, full)
		if len(dirs) >= maxBrowseDirs {
			break
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j])
	})
	return dirs, nil
}

// readable проверяет, что каталог можно открыть и прочитать из него хотя бы запись.
func readable(dir string) bool {
	f, err := os.Open(dir) //nolint:gosec // G703: dir собран из ReadDir уже проверенного каталога
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	return err == nil || errors.Is(err, io.EOF)
}
