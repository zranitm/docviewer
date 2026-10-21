package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel/trace"

	"docviewer/internal/telemetry"
)

const (
	routeUnmatched = "unmatched"
	corsMaxAge     = 12 * time.Hour
)

// requestLogger writes one line per request and records its duration: the
// route template, never the path or query, since both carry the VIN (LLD
// section 8), with the trace ID so the line and the span can be matched. It
// sits outside recovery, so a panic is logged with the 500 it became. Health
// probes are logged at debug: under an orchestrator they would drown the rest.
func requestLogger(log *slog.Logger, metrics *telemetry.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		took := time.Since(start)
		route := c.FullPath()
		if route == "" {
			route = routeUnmatched
		}
		spanCtx := spanContextOf(c)
		ctx := trace.ContextWithSpanContext(c.Request.Context(), spanCtx)
		status := c.Writer.Status()
		metrics.HTTPRequest(ctx, route, status, took)
		attrs := []any{"method", c.Request.Method, "route", route, "status", status, "duration_ms", took.Milliseconds()}
		if spanCtx.HasTraceID() {
			attrs = append(attrs, "trace_id", spanCtx.TraceID().String())
		}
		level := slog.LevelInfo
		if route == healthzPath || route == readyzPath {
			level = slog.LevelDebug
		}
		log.Log(ctx, level, "request", attrs...)
	}
}

// keySpanContext is where rememberSpan leaves the request's span context for
// the logger, which runs outside the tracing middleware and so never sees
// its span: the log line gets the trace ID and the metric its exemplar.
const keySpanContext = "docviewer.span_context"

// rememberSpan runs just inside tracing: it keeps the span context in the
// Gin context and names the trace in the response header before anything
// is written.
func rememberSpan() gin.HandlerFunc {
	return func(c *gin.Context) {
		if sc := trace.SpanContextFromContext(c.Request.Context()); sc.IsValid() {
			c.Set(keySpanContext, sc)
			if sc.IsSampled() { // the header names only a trace that is exported
				c.Header(HeaderTraceID, sc.TraceID().String())
			}
		}
		c.Next()
	}
}

// spanContextOf is the span context rememberSpan kept, or an empty one.
func spanContextOf(c *gin.Context) trace.SpanContext {
	sc, _ := c.Get(keySpanContext)
	spanCtx, _ := sc.(trace.SpanContext)
	return spanCtx
}

// tracing opens a server span per request, named by route, except for the
// health checks, which would only be noise.
func tracing(serviceName string, tel *telemetry.Telemetry) gin.HandlerFunc {
	return otelgin.Middleware(serviceName,
		otelgin.WithTracerProvider(tel.TracerProvider()),
		otelgin.WithMeterProvider(tel.MeterProvider()),
		otelgin.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != healthzPath && r.URL.Path != readyzPath
		}),
	)
}

// corsMiddleware allows the configured browser origins to call the API and
// answers their preflight requests. Nothing is installed when the allow-list
// is empty: no browser origin may call the API.
func corsMiddleware(origins []string) gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:  origins,
		AllowMethods:  []string{http.MethodGet, http.MethodOptions},
		AllowHeaders:  []string{"Origin", "Accept", "Content-Type"},
		ExposeHeaders: []string{HeaderTraceID},
		MaxAge:        corsMaxAge,
	})
}
