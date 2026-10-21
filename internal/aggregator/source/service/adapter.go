// Package service adapts the Service System API client to source.Source:
// positions become the client's paging parameters, client errors go through
// errmap, and wire records become domain documents (mapping.go).
package service

import (
	"context"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/errmap"
	"docviewer/internal/client/serviceapi"
	"docviewer/internal/model"
	"docviewer/pkg/httpjson"
)

// Name is this source's name in config, cursors and responses.
const Name model.Source = "service"

// Lister is what the adapter needs from the Service client: one page of
// records. It sits here, on the consumer's side, so tests can fake it.
type Lister interface {
	ListRecords(ctx context.Context, vin string, p serviceapi.ListParams) (serviceapi.RecordPage, error)
}

// Adapter is the Service System as a Source.
type Adapter struct{ c Lister }

// New wraps a Service client.
func New(c Lister) *Adapter { return &Adapter{c: c} }

// Build creates the Service client and its adapter: the one call wiring needs.
func Build(baseURL string, hc httpjson.Client) (source.Source, error) {
	c, err := serviceapi.New(baseURL, hc)
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// Name returns Name.
func (a *Adapter) Name() model.Source { return Name }

// FetchPage asks the Service System for req.Limit records after req.After.
func (a *Adapter) FetchPage(ctx context.Context, req source.Request) (source.Page, error) {
	p := serviceapi.ListParams{Limit: req.Limit}
	if req.After != nil {
		p.BeforeTS = req.After.IssuedAt.Unix()
		p.BeforeID = model.UpstreamID(Name, req.After.ID)
	}
	resp, err := a.c.ListRecords(ctx, string(req.VIN), p)
	if err != nil {
		return errmap.FromClient(err)
	}
	return toPage(resp), nil
}
