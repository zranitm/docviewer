// Package sales adapts the Sales System API client to source.Source:
// positions become the client's paging parameters, client errors go through
// errmap, and wire documents become domain documents (mapping.go).
package sales

import (
	"context"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/errmap"
	"docviewer/internal/client/salesapi"
	"docviewer/internal/model"
	"docviewer/pkg/httpjson"
)

// Name is this source's name in config, cursors and responses.
const Name model.Source = "sales"

// Lister is what the adapter needs from the Sales client: one page of
// documents. It sits here, on the consumer's side, so tests can fake it.
type Lister interface {
	ListDocuments(ctx context.Context, vin string, p salesapi.ListParams) (salesapi.DocumentPage, error)
}

// Adapter is the Sales System as a Source.
type Adapter struct{ c Lister }

// New wraps a Sales client.
func New(c Lister) *Adapter { return &Adapter{c: c} }

// Build creates the Sales client and its adapter: the one call wiring needs.
func Build(baseURL string, hc httpjson.Client) (source.Source, error) {
	c, err := salesapi.New(baseURL, hc)
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// Name returns Name.
func (a *Adapter) Name() model.Source { return Name }

// FetchPage asks the Sales System for req.Limit documents after req.After.
func (a *Adapter) FetchPage(ctx context.Context, req source.Request) (source.Page, error) {
	p := salesapi.ListParams{Limit: req.Limit}
	if req.After != nil {
		p.BeforeDate = req.After.IssuedAt.Format(time.DateOnly)
		p.BeforeID = model.UpstreamID(Name, req.After.ID)
	}
	resp, err := a.c.ListDocuments(ctx, string(req.VIN), p)
	if err != nil {
		return errmap.FromClient(err)
	}
	return toPage(req.VIN, resp)
}
