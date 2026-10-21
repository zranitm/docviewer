package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"docviewer/internal/config"
)

// Routes.
const (
	ServiceName = "docviewer" // the tracing service name and the server span prefix

	// HeaderTraceID names the request's trace on every traced response, so a
	// reported failure can be matched to its trace and log lines (HLD decision
	// D-14, failure correlation).
	HeaderTraceID = "X-Trace-Id"

	paramVIN      = "vin"
	documentsPath = "/v1/vehicles/:" + paramVIN + "/documents"
	healthzPath   = "/healthz"
	readyzPath    = "/readyz"

	readHeaderTimeout = 5 * time.Second
)

// Server is the Gin router, its middleware and routes, inside an http.Server.
type Server struct {
	engine *gin.Engine
	cfg    config.Server
	log    *slog.Logger
	ready  atomic.Bool
}

// NewServer builds the router, outermost first: request logging with metrics
// (outside recovery, so it sees the status a panic ends with), recovery (a
// panic becomes internal_error), tracing, CORS when origins are configured,
// then the routes.
func NewServer(cfg config.Server, deps *Dependencies) *Server {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{engine: gin.New(), cfg: cfg, log: deps.Log}
	h := &Handler{agg: deps.Aggregator, log: deps.Log, ready: &s.ready}
	s.engine.Use(
		requestLogger(deps.Log, deps.Telemetry.Metrics()),
		gin.CustomRecoveryWithWriter(io.Discard, h.recovered),
		tracing(ServiceName, deps.Telemetry),
		rememberSpan(),
	)
	if len(cfg.CORSOrigins) > 0 {
		s.engine.Use(corsMiddleware(cfg.CORSOrigins))
	}
	s.engine.GET(healthzPath, h.Healthz)
	s.engine.GET(readyzPath, h.Readyz)
	s.engine.GET(documentsPath, h.ListDocuments)
	s.engine.NoRoute(h.NoRoute)
	return s
}

// Handler is the router, for tests and for any caller that brings its own
// listener.
func (s *Server) Handler() http.Handler { return s.engine }

// Run listens on the configured address and serves until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "listening", "addr", ln.Addr().String())
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx ends, then turns readiness off and drains
// in-flight requests for up to the shutdown grace period.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.engine,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	s.ready.Store(true)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		s.ready.Store(false)
		return err
	case <-ctx.Done():
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
