// Package client holds what every upstream client shares: the sentinel
// errors callers check with errors.Is, and FromHTTP, which turns HTTP
// outcomes into them. No code outside the client packages inspects a status
// code.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"docviewer/pkg/httpjson"
)

// Sentinel errors, one per upstream outcome the adapters care about.
var (
	ErrNotFound    = errors.New("upstream: not found")          // 404
	ErrRejected    = errors.New("upstream: request rejected")   // 400, 401, 403, 422
	ErrUnavailable = errors.New("upstream: unavailable")        // 5xx, 429, connection errors, open breaker
	ErrTooLarge    = errors.New("upstream: response too large") // over the size cap
	ErrMalformed   = errors.New("upstream: malformed response") // body doesn't decode
)

// rejectedStatuses are the responses that say the request itself is wrong.
var rejectedStatuses = []int{
	http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity,
}

// FromHTTP wraps an httpjson error in the matching sentinel, keeping the
// original for logs. Context errors pass through unchanged, so deadlines and
// cancellations stay recognisable.
func FromHTTP(err error) error {
	var se *httpjson.StatusError
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &se) && se.Code == http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.As(err, &se) && slices.Contains(rejectedStatuses, se.Code):
		return fmt.Errorf("%w: %w", ErrRejected, err)
	case errors.Is(err, httpjson.ErrTooLarge):
		return fmt.Errorf("%w: %w", ErrTooLarge, err)
	case errors.Is(err, httpjson.ErrDecode):
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}
