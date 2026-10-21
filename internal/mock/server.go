package mock

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Paging rules both mocks share.
const (
	defaultLimit = 20
	maxLimit     = 100
	paramLimit   = "limit"

	filesPath       = "/files"
	healthPath      = "/healthz"
	adminFaultsPath = "/admin/faults"

	readHeaderTimeout = 5 * time.Second
	shutdownGrace     = 5 * time.Second
)

// Config is a mock server's own small schema, loaded like the service's.
type Config struct {
	Server        Server `mapstructure:"server"`
	PublicBaseURL string `mapstructure:"public_base_url" validate:"required,url"` // used in document links, which the browser opens
	Faults        Fault  `mapstructure:"faults"`                                  // startup defaults; POST /admin/faults changes them live
}

// Server is the listener.
type Server struct {
	Addr string `mapstructure:"addr" validate:"required"`
}

// Validate checks the startup fault's ranges.
func (c *Config) Validate() error { return c.Faults.Validate() }

// Serve runs h on ln until ctx ends, then drains for a short grace period.
// Request contexts descend from ctx, so a hanging fault ends with the server.
func Serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// fileURL is a document's link on the mock itself.
func fileURL(publicBaseURL, id string) string {
	return strings.TrimSuffix(publicBaseURL, "/") + filesPath + "/" + id + ".pdf"
}

// commonRoutes adds what both mocks serve besides their API: the
// placeholder file, the fault controls and a health check.
func commonRoutes(mux *http.ServeMux, faults *Faults) {
	mux.HandleFunc("GET "+filesPath+"/{file}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", strconv.Itoa(len(PlaceholderPDF)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(PlaceholderPDF)
	})
	mux.Handle(adminFaultsPath, faults.AdminHandler())
	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// parseLimit reads the page size: default 20, 1 to 100.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLimit {
		return 0, errors.New("limit must be an integer from 1 to 100")
	}
	return n, nil
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// logging writes one line per request.
func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status before passing it on.
func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// CORS headers: the mocks are test doubles that a browser demo drives
// directly (the fault controls), so any origin may call any route.
const (
	corsAllowMethods = "GET, POST, DELETE, OPTIONS"
	corsAllowHeaders = "Content-Type"
	corsMaxAge       = "600"
)

// cors answers preflight requests and lets any origin read every response.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
