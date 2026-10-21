// Package dto is the HTTP API's public contract: request parameters,
// response JSON, status names and error codes, mapped from the domain and
// the aggregator's results. Nothing here leaks into other layers.
package dto

import (
	"errors"
	"time"

	"docviewer/internal/aggregator"
	"docviewer/internal/model"
)

// DocumentPage is the response body of GET /v1/vehicles/{vin}/documents.
type DocumentPage struct {
	VIN        string         `json:"vin"`
	Documents  []Document     `json:"documents"`
	NextCursor *string        `json:"next_cursor"` // null on the last page
	Sources    []SourceStatus `json:"sources"`
}

// Document is one row of the consolidated list.
type Document struct {
	Type     string      `json:"type"`
	Title    string      `json:"title"`
	Summary  string      `json:"summary,omitempty"`
	IssuedAt time.Time   `json:"issued_at"`
	Sources  []SourceRef `json:"sources"`
}

// SourceRef is one system's copy of a document.
type SourceRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	URL    string `json:"url"`
}

// SourceStatus is how one source fared for this page.
type SourceStatus struct {
	Source string `json:"source"`
	Status string `json:"status"`
}

// statusNames are the wire names of aggregator statuses: the API contract is
// fixed here, not in the aggregator.
var statusNames = map[aggregator.Status]string{
	aggregator.StatusOK:          "ok",
	aggregator.StatusTimeout:     "timeout",
	aggregator.StatusError:       "error",
	aggregator.StatusDone:        "done",
	aggregator.StatusUnavailable: "unavailable",
}

// ErrorCode is a machine-readable error code, part of the API contract.
type ErrorCode string

// The error codes (HLD section 5).
const (
	CodeInvalidVIN         ErrorCode = "invalid_vin"
	CodeInvalidLimit       ErrorCode = "invalid_limit"
	CodeInvalidCursor      ErrorCode = "invalid_cursor"
	CodeCursorOutdated     ErrorCode = "cursor_outdated"
	CodeSourcesUnavailable ErrorCode = "sources_unavailable"
	CodeNotFound           ErrorCode = "not_found"
	CodeInternal           ErrorCode = "internal_error"
)

// ErrorResponse is the body of every error: {"error": {"code", "message"}}.
// Sources is set only for sources_unavailable.
type ErrorResponse struct {
	Error   ErrorDetail    `json:"error"`
	Sources []SourceStatus `json:"sources,omitempty"`
}

// ErrorDetail is the code and a message safe to show.
type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Statuses maps per-source results to the response, in the same order.
func Statuses(rs []aggregator.SourceResult) []SourceStatus {
	out := make([]SourceStatus, 0, len(rs))
	for _, r := range rs {
		name, ok := statusNames[r.Status]
		if !ok {
			name = statusNames[aggregator.StatusError] // a result never filled in is a failure, never ok
		}
		out = append(out, SourceStatus{Source: string(r.Source), Status: name})
	}
	return out
}

// FromEntries maps domain entries to the response shape; never nil, so an
// empty page serialises as [] rather than null.
func FromEntries(es []model.Entry) []Document {
	out := make([]Document, 0, len(es))
	for _, e := range es {
		refs := make([]SourceRef, 0, len(e.Sources))
		for _, s := range e.Sources {
			refs = append(refs, SourceRef{Source: string(s.Source), ID: s.ID, URL: s.URL})
		}
		out = append(out, Document{Type: e.Type, Title: e.Title, Summary: e.Summary, IssuedAt: e.IssuedAt, Sources: refs})
	}
	return out
}

// NewDocumentPage builds the 200 body. next_cursor is null when the result
// has no next cursor.
func NewDocumentPage(res aggregator.Result) DocumentPage {
	var next *string
	if res.NextCursor != "" {
		next = &res.NextCursor
	}
	return DocumentPage{VIN: string(res.VIN), Documents: FromEntries(res.Entries), NextCursor: next, Sources: Statuses(res.Sources)}
}

// ListQuery is the query string of GET /v1/vehicles/{vin}/documents. Gin binds
// and validates it from these tags; the default and bounds are the API
// contract, the same as in the OpenAPI spec.
type ListQuery struct {
	Limit  int    `form:"limit,default=20" binding:"min=1,max=100"`
	Cursor string `form:"cursor"`
}

// ErrInvalidLimit wraps any ListQuery binding error: limit is its only
// validated field.
var ErrInvalidLimit = errors.New("invalid limit")
