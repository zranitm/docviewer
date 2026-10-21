// Package httpjson fetches a URL and decodes its JSON body: context-aware,
// size-capped, with typed status errors. It knows nothing about any
// particular API.
package httpjson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// StatusError is a non-2xx response.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("http status %d", e.Code) }

// Sentinel errors for the two ways a 2xx body can still be unusable.
var (
	ErrTooLarge = errors.New("response body over the size limit")
	ErrDecode   = errors.New("response body doesn't decode")
)

// Client is an HTTP client with a cap on how much of a body it reads.
type Client struct {
	HTTP     *http.Client // its transport carries tracing and resilience
	MaxBytes int64
}

// Get fetches rawURL and decodes a JSON body into T. A non-2xx response returns
// *StatusError without decoding; a body over MaxBytes returns ErrTooLarge.
// Transport errors, including context and open-breaker errors, pass through.
func Get[T any](ctx context.Context, c Client, rawURL string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.MaxBytes)) // lets the connection be reused
		return zero, &StatusError{Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes+1))
	if err != nil {
		return zero, err
	}
	if int64(len(body)) > c.MaxBytes {
		return zero, ErrTooLarge
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return zero, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	return v, nil
}
