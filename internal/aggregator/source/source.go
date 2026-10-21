// Package source is the port the aggregator pages through: Source, Request,
// Position, Page and the port's errors. Adapters implement it; the
// aggregator consumes it.
package source

import (
	"context"
	"errors"
	"fmt"
	"time"

	"docviewer/internal/model"
)

// ErrPermanent marks an upstream failure that retrying won't fix. Adapters
// produce it (through errmap); every other error is transient.
var ErrPermanent = errors.New("permanent upstream error")

// ErrUnsupported is a Request field this source can't honour. It wraps
// ErrPermanent: asking again won't help, and ignoring the field would return
// the wrong documents.
var ErrUnsupported = fmt.Errorf("%w: unsupported request", ErrPermanent)

// Position is where paging stands in one source: the last document
// already returned to the client.
type Position struct {
	IssuedAt time.Time
	ID       string // document ID as returned, e.g. "sales:S-1002"
}

// Page is one page from a source, in that source's own order (newest first).
type Page struct {
	Docs    []model.Document
	HasMore bool
	SkipTo  *Position // set only when every item fetched was malformed (LLD section 6)
}

// Request is one page to fetch. New dimensions (a filter, an order) are added
// as fields whose zero value means "none", so existing adapters keep compiling
// and keep their current behaviour.
type Request struct {
	VIN   model.VIN
	After *Position // nil: from the newest
	Limit int
}

// Source is one upstream system as the aggregator sees it. FetchPage returns
// up to req.Limit documents that come after req.After in the source's order
// (date descending, then the source's own ID order). A field it can't honour
// is ErrUnsupported, never silently ignored.
type Source interface {
	Name() model.Source
	FetchPage(ctx context.Context, req Request) (Page, error)
}
