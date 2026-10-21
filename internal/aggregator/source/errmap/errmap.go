// Package errmap maps client sentinel errors to Source semantics, shared by
// every adapter: not found means done; rejected, too large and malformed are
// permanent; everything else is transient.
package errmap

import (
	"errors"
	"fmt"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/client"
)

// FromClient handles a failed paging call. Not found means the source has
// nothing for this VIN: an empty, final page. Rejected, too large and
// malformed wrap source.ErrPermanent, since the same request would fail the
// same way. Everything else (unavailable, context errors) is transient.
func FromClient(err error) (source.Page, error) {
	switch {
	case errors.Is(err, client.ErrNotFound):
		return source.Page{}, nil
	case errors.Is(err, client.ErrRejected), errors.Is(err, client.ErrTooLarge), errors.Is(err, client.ErrMalformed):
		return source.Page{}, fmt.Errorf("%w: %w", source.ErrPermanent, err)
	default:
		return source.Page{}, err
	}
}
