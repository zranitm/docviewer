// Package salesapi is the Sales System API client: endpoints, query
// parameters and wire models, nothing about paging rules or the domain.
package salesapi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"docviewer/internal/client"
	"docviewer/pkg/httpjson"
)

// Query parameters and paths of the Sales System API.
const (
	documentsPath = "api/v1/vehicles" // + /{vin}/documents
	paramLimit    = "limit"
	paramBefore   = "before"
	beforeSep     = "|" // before=<date>|<id>
)

// ListParams pages newest first: by date, then document ID.
type ListParams struct {
	Limit      int
	BeforeDate string // YYYY-MM-DD; empty starts from the newest
	BeforeID   string
}

// Client calls one Sales System API.
type Client struct {
	base *url.URL
	hc   httpjson.Client
}

// New parses baseURL once, so a bad URL fails at startup, not on the first call.
func New(baseURL string, hc httpjson.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("sales base URL: %w", err)
	}
	return &Client{base: u, hc: hc}, nil
}

// ListDocuments returns one page of a vehicle's documents. Errors are client
// sentinels (client.ErrNotFound, client.ErrRejected, ...), never raw HTTP codes.
func (c *Client) ListDocuments(ctx context.Context, vin string, p ListParams) (DocumentPage, error) {
	q := url.Values{paramLimit: {strconv.Itoa(p.Limit)}}
	if p.BeforeDate != "" {
		q.Set(paramBefore, p.BeforeDate+beforeSep+p.BeforeID)
	}
	u := c.base.JoinPath(documentsPath, vin, "documents")
	u.RawQuery = q.Encode()
	page, err := httpjson.Get[DocumentPage](ctx, c.hc, u.String())
	return page, client.FromHTTP(err)
}
