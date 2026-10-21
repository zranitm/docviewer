// Package serviceapi is the Service System API client: endpoints, query
// parameters and wire models, nothing about paging rules or the domain.
package serviceapi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"docviewer/internal/client"
	"docviewer/pkg/httpjson"
)

// Query parameters and paths of the Service System API.
const (
	recordsPath   = "v2/records"
	paramVIN      = "vin"
	paramLimit    = "limit"
	paramBeforeTS = "before_ts"
	paramBeforeID = "before_id"
)

// ListParams pages newest first: by timestamp, then numeric record ID.
type ListParams struct {
	Limit    int
	BeforeTS int64  // epoch seconds; sent with BeforeID
	BeforeID string // empty starts from the newest
}

// Client calls one Service System API.
type Client struct {
	base *url.URL
	hc   httpjson.Client
}

// New parses baseURL once, so a bad URL fails at startup, not on the first call.
func New(baseURL string, hc httpjson.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("service base URL: %w", err)
	}
	return &Client{base: u, hc: hc}, nil
}

// ListRecords returns one page of a vehicle's records. Errors are client
// sentinels (client.ErrNotFound, client.ErrRejected, ...), never raw HTTP codes.
func (c *Client) ListRecords(ctx context.Context, vin string, p ListParams) (RecordPage, error) {
	q := url.Values{paramVIN: {vin}, paramLimit: {strconv.Itoa(p.Limit)}}
	if p.BeforeID != "" {
		q.Set(paramBeforeTS, strconv.FormatInt(p.BeforeTS, 10))
		q.Set(paramBeforeID, p.BeforeID)
	}
	u := c.base.JoinPath(recordsPath)
	u.RawQuery = q.Encode()
	page, err := httpjson.Get[RecordPage](ctx, c.hc, u.String())
	return page, client.FromHTTP(err)
}
