package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"docviewer/internal/aggregator"
	"docviewer/internal/handler/api/dto"
	"docviewer/internal/model"
)

// StatusClientClosedRequest is nginx's 499: the client went away before an
// answer. Nothing is sent; it keeps a hang-up out of the 200 bucket in logs
// and metrics.
const StatusClientClosedRequest = 499

// The HTTP layer's own errors: an unknown route, and a recovered panic.
var (
	errNotFound = errors.New("not found")
	errPanic    = errors.New("handler panicked")
)

// Fixed messages. 5xx messages never describe the failure, so internals
// never leak; the invalid-cursor message never echoes the cursor, which
// carries the VIN.
const (
	msgInvalidLimit   = "limit must be an integer from 1 to 100"
	msgInvalidCursor  = "invalid cursor"
	msgCursorOutdated = "a source was added; start again from the first page"
	msgNotFound       = "not found"
	msgInternal       = "internal error"
	msgRetryable      = "no source answered; retry with the same cursor"
	msgNotRetryable   = "no source can answer; retrying won't help"
)

// errorMap maps known errors to responses; anything else is a 500.
var errorMap = []struct {
	target  error
	status  int
	code    dto.ErrorCode
	message string // fixed message; empty means the error's own, which describes the request
}{
	{model.ErrInvalidVIN, http.StatusBadRequest, dto.CodeInvalidVIN, ""},
	{dto.ErrInvalidLimit, http.StatusBadRequest, dto.CodeInvalidLimit, msgInvalidLimit},
	{aggregator.ErrInvalidCursor, http.StatusBadRequest, dto.CodeInvalidCursor, msgInvalidCursor},
	{aggregator.ErrCursorOutdated, http.StatusBadRequest, dto.CodeCursorOutdated, msgCursorOutdated},
	{errNotFound, http.StatusNotFound, dto.CodeNotFound, msgNotFound},
}

// writeError turns err into a JSON error response. A 4xx message is the
// mapping's fixed text, or the error's own when it describes the request (an
// invalid VIN names the rule it broke); binding and cursor errors always get
// fixed text. 5xx messages are fixed, so internals never leak.
func (h *Handler) writeError(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) {
		h.log.DebugContext(c.Request.Context(), "client went away", "route", c.FullPath())
		c.AbortWithStatus(StatusClientClosedRequest) // no one to answer; the status is for logs and metrics only
		return
	}
	var su *aggregator.SourcesUnavailableError
	if errors.As(err, &su) {
		msg := msgNotRetryable
		if su.Retryable() {
			msg = msgRetryable
		}
		c.AbortWithStatusJSON(http.StatusBadGateway, dto.ErrorResponse{
			Error:   dto.ErrorDetail{Code: dto.CodeSourcesUnavailable, Message: msg},
			Sources: dto.Statuses(su.Sources),
		})
		return
	}
	for _, m := range errorMap {
		if errors.Is(err, m.target) {
			c.AbortWithStatusJSON(m.status, dto.ErrorResponse{Error: dto.ErrorDetail{Code: m.code, Message: cmp.Or(m.message, err.Error())}})
			return
		}
	}
	h.log.ErrorContext(c.Request.Context(), "unhandled error", "route", c.FullPath(), "err", err)
	c.AbortWithStatusJSON(http.StatusInternalServerError,
		dto.ErrorResponse{Error: dto.ErrorDetail{Code: dto.CodeInternal, Message: msgInternal}})
}
