package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"docviewer/internal/aggregator"
	"docviewer/internal/handler/api/dto"
	"docviewer/internal/model"
)

// Lister is what the handlers need from the aggregator: one page per call.
// It sits here, on the consumer's side, so handler tests can fake it.
type Lister interface {
	List(ctx context.Context, req aggregator.ListRequest) (aggregator.Result, error)
}

// Handler holds the injected aggregator and logger; nothing uses the global
// slog logger.
type Handler struct {
	agg   Lister
	log   *slog.Logger
	ready *atomic.Bool // owned by the Server: true while serving
}

// ListDocuments serves GET /v1/vehicles/:vin/documents.
func (h *Handler) ListDocuments(c *gin.Context) {
	vin, err := model.ParseVIN(c.Param(paramVIN)) // normalises and checks: a domain rule, not a binding tag
	if err != nil {
		h.writeError(c, err)
		return
	}
	var q dto.ListQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		h.writeError(c, fmt.Errorf("%w: %w", dto.ErrInvalidLimit, err))
		return
	}
	res, err := h.agg.List(c.Request.Context(), aggregator.ListRequest{
		Query:  aggregator.Query{VIN: vin},
		Cursor: q.Cursor,
		Limit:  q.Limit,
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewDocumentPage(res))
}

// Healthz is liveness: the process answers.
func (h *Handler) Healthz(c *gin.Context) { c.Status(http.StatusOK) }

// Readyz is readiness: started and not shutting down.
func (h *Handler) Readyz(c *gin.Context) {
	if h.ready.Load() {
		c.Status(http.StatusOK)
		return
	}
	c.Status(http.StatusServiceUnavailable)
}

// NoRoute answers an unknown route with the JSON not_found error.
func (h *Handler) NoRoute(c *gin.Context) { h.writeError(c, errNotFound) }

// recovered turns a panic into the fixed internal_error response.
func (h *Handler) recovered(c *gin.Context, recovered any) {
	h.log.ErrorContext(c.Request.Context(), "handler panicked", "route", c.FullPath(), "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
	h.writeError(c, errPanic)
}
