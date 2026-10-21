package server

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IliaSA101/aiexport/internal/bundle"
	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/IliaSA101/aiexport/internal/plan"
)

// maxSkippedInResponse — сколько пропущенных записей отдаём в таблицу. Полный счёт
// остаётся в totals и skipByReason, так что усечение видно по цифрам.
const maxSkippedInResponse = 500

// SourceStat — итог по одному источнику; порядок совпадает с Spec.Sources.
type SourceStat struct {
	Label  string     `json:"label"`
	Path   string     `json:"path"`
	Mode   model.Mode `json:"mode"`
	Files  int        `json:"files"`
	Tokens int        `json:"tokens"`
}

// PlanResponse — ответ /api/plan.
type PlanResponse struct {
	Totals       model.Totals      `json:"totals"`
	Sources      []SourceStat      `json:"sources"`
	Files        []model.FileEntry `json:"files"`   // только включённые
	Skipped      []model.FileEntry `json:"skipped"` // первые maxSkippedInResponse, без mode-tree
	SkipByReason map[string]int    `json:"skipByReason"`
}

// ExportRequest — Spec плюс параметры вывода. Embedded-структура разворачивается в
// JSON плоско, поэтому UI шлёт обычный Spec с двумя дополнительными полями.
type ExportRequest struct {
	model.Spec
	Out      string `json:"out"`      // пусто -> имя выбирает сервер, каталог — текущий
	Download bool   `json:"download"` // true -> тело файла в ответе, на диск не пишем
	Force    bool   `json:"force"`    // true -> заменить существующий файл вывода
}

// ExportResponse — ответ /api/export при записи на диск.
type ExportResponse struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Tokens int    `json:"tokens"`
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var spec model.Spec
	if !readJSON(w, r, &spec) {
		return
	}
	p, ok := s.build(r.Context(), w, spec, "")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, planResponse(p))
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req ExportRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()

	var outAbs string
	if req.Download {
		outAbs = "" // на диск не пишем, исключать из обхода нечего
	} else {
		var err error
		if outAbs, err = resolveOut(req.Out, time.Now(), req.Force); err != nil {
			if errors.Is(err, errOutExists) {
				// 409 только для этой ситуации; code однозначен, даже если 409 появится ещё где-то.
				writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error(), Code: codeFileExists})
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	p, ok := s.build(ctx, w, req.Spec, outAbs)
	if !ok {
		return
	}

	if req.Download {
		s.download(ctx, w, p)
		return
	}
	if err := bundle.WriteFile(ctx, p, outAbs); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.log.Error("запись бандла", "err", err)
		writeError(w, http.StatusInternalServerError, "не удалось записать бандл: "+err.Error())
		return
	}
	st, err := os.Stat(outAbs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "бандл записан, но не читается обратно: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ExportResponse{Path: outAbs, Bytes: st.Size(), Tokens: p.Totals.Tokens})
}

// download отдаёт бандл телом ответа. Ошибка посреди потока уже не может сменить
// код ответа, поэтому она только логируется: клиент увидит обрыв.
func (s *Server) download(ctx context.Context, w http.ResponseWriter, p model.Plan) {
	name := defaultOutName(time.Now())
	h := w.Header()
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.WriteHeader(http.StatusOK)
	if err := bundle.Render(ctx, p, w); err != nil && ctx.Err() == nil {
		s.log.Error("отдача бандла", "err", err)
	}
}

// build проверяет Spec, строит план и при ошибке сам отвечает клиенту.
// outAbs — файл вывода (если он внутри источника, его нужно исключить из обхода).
func (s *Server) build(ctx context.Context, w http.ResponseWriter, spec model.Spec, outAbs string) (model.Plan, bool) {
	spec, err := plan.Normalize(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return model.Plan{}, false
	}
	for i, src := range spec.Sources {
		st, serr := os.Stat(src.Path)
		if serr != nil || !st.IsDir() {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("источник %d («%s»): папка не найдена или недоступна", i+1, src.Label))
			return model.Plan{}, false
		}
	}
	if outAbs != "" {
		spec.Exclude = append(spec.Exclude, plan.ExcludeOutput(spec.Sources, outAbs)...)
	}

	if !s.acquire(ctx) {
		return model.Plan{}, false // клиент ушёл, отвечать некому
	}
	defer s.release()

	p, err := plan.Build(ctx, s.counter, spec)
	if err != nil {
		if ctx.Err() != nil {
			return model.Plan{}, false
		}
		s.log.Error("построение плана", "err", err)
		writeError(w, http.StatusInternalServerError, "не удалось построить план: "+err.Error())
		return model.Plan{}, false
	}
	return p, true
}

// planResponse сворачивает план в ответ UI. Каталог, отсечённый целиком, — одна
// запись, и в таблице пропущенного он тоже одна строка.
func planResponse(p model.Plan) PlanResponse {
	resp := PlanResponse{
		Totals:       p.Totals,
		Sources:      make([]SourceStat, len(p.Spec.Sources)),
		Files:        []model.FileEntry{},
		Skipped:      []model.FileEntry{},
		SkipByReason: map[string]int{},
	}
	for i, src := range p.Spec.Sources {
		resp.Sources[i] = SourceStat{Label: src.Label, Path: src.Path, Mode: src.Mode}
	}
	for _, f := range p.Files {
		if f.Status == model.StatusIncluded {
			resp.Files = append(resp.Files, f)
			if f.Source >= 0 && f.Source < len(resp.Sources) {
				resp.Sources[f.Source].Files++
				resp.Sources[f.Source].Tokens += f.Tokens
			}
			continue
		}
		resp.SkipByReason[string(f.Reason)]++
		// mode-tree в список не идёт: на большом tree-источнике эти записи съели бы
		// весь лимит, и настоящие пропуски (бинарники, превышения, секреты) в
		// таблицу не попали бы. Счёт по ним остаётся в totals.treeOnly и skipByReason.
		if f.Reason == model.ReasonModeTree {
			continue
		}
		if len(resp.Skipped) < maxSkippedInResponse {
			resp.Skipped = append(resp.Skipped, f)
		}
	}
	return resp
}

// errOutExists — признак для errors.Is: файл вывода существует, force не задан.
var errOutExists = errors.New("файл уже существует")

func defaultOutName(t time.Time) string {
	return "aiexport-" + t.Format("2006-01-02-1504") + ".md"
}

// resolveOut превращает поле out в абсолютный путь и проверяет, что туда можно писать.
// Родительский каталог не создаётся: опечатка в пути не должна плодить папки.
// Существующий файл без force не заменяется — как и в CLI: запись атомарной
// подменой стёрла бы чужие данные без единого предупреждения. Запрет действует
// и для имени по умолчанию (две выгрузки за одну минуту дают одно имя).
func resolveOut(out string, now time.Time, force bool) (string, error) {
	var p string
	if out == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("текущий каталог не определён, укажите путь файла явно: %w", err)
		}
		p = filepath.Join(cwd, defaultOutName(now))
	} else {
		var err error
		if p, err = cleanLocalPath(out, true); err != nil {
			return "", fmt.Errorf("путь файла: %w", err)
		}
	}
	if st, serr := os.Lstat(p); serr == nil {
		switch {
		case st.IsDir():
			return "", fmt.Errorf("путь файла: %q — это папка, нужно имя файла", filepath.Base(p))
		case !force:
			return "", fmt.Errorf("%w: %q — подтвердите перезапись или укажите другое имя", errOutExists, p)
		}
	}
	if st, serr := os.Stat(filepath.Dir(p)); serr != nil || !st.IsDir() {
		return "", fmt.Errorf("путь файла: папка %q не найдена", filepath.Dir(p))
	}
	return p, nil
}
