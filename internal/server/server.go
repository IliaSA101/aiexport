// Пакет server — локальный HTTP API и раздача вшитого веб-UI.
//
// Инструмент отдаёт содержимое диска по HTTP, поэтому защита здесь не формальность:
// только 127.0.0.1, проверка Host (DNS rebinding), проверка Origin и Content-Type
// у POST (кросс-сайтовая запись файлов), CSP на страницу.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IliaSA101/aiexport/internal/tokens"
)

const (
	// DefaultPort — рабочий порт UI. Тестовые прогоны берут другой через --port.
	DefaultPort = 8471

	maxBodyBytes    = 1 << 20 // Spec с сотнями масок и инструкциями — килобайты, мегабайт с запасом
	maxBuilds       = 2       // одновременных построений плана: каждое читает диск и грузит CPU
	shutdownTimeout = 5 * time.Second
)

// Options — параметры сервера.
type Options struct {
	Port    int             // 1..65535
	Counter *tokens.Counter // создаётся один раз на процесс: загрузка словаря 0,3–0,8 с
	Web     fs.FS           // корень веб-UI, index.html лежит в корне
	Logger  *slog.Logger    // nil -> slog.Default()
}

// Server — HTTP-сервер aiexport. После New не меняется, поэтому общих замков не нужно:
// единственное разделяемое изменяемое состояние — семафор построений (канал).
type Server struct {
	port    int
	counter *tokens.Counter
	static  http.Handler
	log     *slog.Logger
	builds  chan struct{}
	hosts   map[string]struct{} // допустимые значения заголовка Host
	origins map[string]struct{} // допустимые значения заголовка Origin
}

// New проверяет параметры и собирает сервер.
func New(opt Options) (*Server, error) {
	if opt.Port < 1 || opt.Port > 65535 {
		return nil, fmt.Errorf("порт %d вне диапазона 1..65535", opt.Port)
	}
	if opt.Counter == nil {
		return nil, errors.New("не задан счётчик токенов")
	}
	if opt.Web == nil {
		return nil, errors.New("не задан веб-UI")
	}
	lg := opt.Logger
	if lg == nil {
		lg = slog.Default()
	}
	port := strconv.Itoa(opt.Port)
	s := &Server{
		port:    opt.Port,
		counter: opt.Counter,
		static:  staticHandler(opt.Web),
		log:     lg,
		builds:  make(chan struct{}, maxBuilds),
		hosts:   map[string]struct{}{},
		origins: map[string]struct{}{},
	}
	for _, h := range []string{"127.0.0.1:" + port, "localhost:" + port} {
		s.hosts[h] = struct{}{}
		s.origins["http://"+h] = struct{}{}
	}
	return s, nil
}

// URL — адрес, по которому UI открывается в браузере.
func (s *Server) URL() string { return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/" }

// Handler возвращает корневой обработчик со всеми проверками безопасности.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/plan", s.method(http.MethodPost, s.handlePlan))
	mux.HandleFunc("/api/export", s.method(http.MethodPost, s.handleExport))
	mux.HandleFunc("/api/browse", s.method(http.MethodGet, s.handleBrowse))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "неизвестный адрес API")
	})
	mux.Handle("/", s.static)
	return s.guard(mux)
}

// Run слушает 127.0.0.1:порт и обслуживает запросы до отмены ctx, затем
// корректно завершается. ready вызывается после успешного bind, до первого запроса.
func (s *Server) Run(ctx context.Context, ready func(url string)) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", addr)
	if err != nil {
		// Молча уйти на другой порт нельзя: UI в закладках и QA ждут конкретный адрес.
		return fmt.Errorf("порт %d занят или недоступен (задайте другой через --port): %w", s.port, err)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		// Запросы наследуют ctx: SIGINT прерывает и долгий обход дерева, а не ждёт его.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if ready != nil {
		ready(s.URL())
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("HTTP-сервер: %w", err)
	case <-ctx.Done():
	}
	// Контекст уже отменён, поэтому таймаут строим от его «безотменной» копии.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("остановка сервера: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP-сервер: %w", err)
	}
	return nil
}

// guard — проверки, общие для всех запросов.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		// Host проверяется первым и на каждом запросе: при DNS rebinding браузер
		// считает чужое имя «своим» и шлёт запросы на наш адрес с этим именем в Host.
		if _, ok := s.hosts[strings.ToLower(r.Host)]; !ok {
			writeError(w, http.StatusForbidden, "недопустимый заголовок Host: доступ только через 127.0.0.1 или localhost")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// Без этого любая страница в браузере пользователя могла бы отправить
			// форму на наш порт и заставить записать файл: Host при этом верный.
			// application/json делает запрос «непростым» (нужен preflight, а мы его не разрешаем).
			if o := r.Header.Get("Origin"); o != "" {
				if _, ok := s.origins[strings.ToLower(o)]; !ok {
					writeError(w, http.StatusForbidden, "запрос с чужого источника (Origin) отклонён")
					return
				}
			}
			if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "ожидается Content-Type: application/json")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// method пропускает только один HTTP-метод и отвечает на остальные JSON-ошибкой.
func (s *Server) method(m string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m {
			w.Header().Set("Allow", m)
			writeError(w, http.StatusMethodNotAllowed, "метод "+r.Method+" не поддерживается, нужен "+m)
			return
		}
		h(w, r)
	}
}

// acquire занимает слот построения; false — клиент ушёл или сервер останавливается.
func (s *Server) acquire(ctx context.Context) bool {
	select {
	case s.builds <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) release() { <-s.builds }

// writeJSON отдаёт значение как JSON. Ошибку записи можно только проигнорировать:
// заголовки уже ушли, клиент отвалился.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// errorResponse — тело ошибки. Code — машинный признак для UI: тексты меняются,
// и опираться на них нельзя.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// codeFileExists — Code ответа 409, когда файл вывода уже есть, а force не задан.
const codeFileExists = "file-exists"

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorResponse{Error: msg})
}

// readJSON читает тело с лимитом размера; при неудаче сам отвечает ошибкой.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "тело запроса слишком большое")
			return false
		}
		writeError(w, http.StatusBadRequest, "тело запроса не разобрано как JSON: "+err.Error())
		return false
	}
	return true
}
