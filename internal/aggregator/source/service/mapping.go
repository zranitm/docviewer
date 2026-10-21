package service

import (
	"strconv"
	"strings"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/client/serviceapi"
	"docviewer/internal/model"
)

// toPage maps a wire page to the domain (LLD section 6, Service mapping),
// skipping malformed items.
func toPage(resp serviceapi.RecordPage) source.Page {
	page := source.Page{HasMore: resp.HasMore}
	var last *source.Position // the last item that can at least be placed
	for _, r := range resp.Records {
		doc, ok := toDocument(r)
		if ok {
			page.Docs = append(page.Docs, doc)
			continue
		}
		if pos, ok := position(r); ok {
			last = &pos
		}
	}
	if len(page.Docs) == 0 && len(resp.Records) > 0 {
		page.SkipTo = last // step past a fully malformed page instead of asking for it forever
	}
	return page
}

// toDocument maps one wire record. ok is false for a malformed item: one
// missing its ID, its timestamp or its link (HLD H-03).
func toDocument(r serviceapi.Record) (model.Document, bool) {
	if r.RecordID <= 0 || r.CreatedAt <= 0 || r.DownloadLink == "" {
		return model.Document{}, false
	}
	return model.Document{
		ID:       model.DocumentID(Name, upstreamID(r)),
		Source:   Name,
		Type:     model.CanonicalType(r.Category),
		Title:    r.Description,
		Summary:  strings.TrimSpace(r.WorkNotes),
		IssuedAt: time.Unix(r.CreatedAt, 0).UTC(),
		URL:      r.DownloadLink,
		Checksum: model.Checksum(r.FileSHA256),
	}, true
}

// position is where r sits in the Service order, if it has an ID and a timestamp.
func position(r serviceapi.Record) (source.Position, bool) {
	if r.RecordID <= 0 || r.CreatedAt <= 0 {
		return source.Position{}, false
	}
	return source.Position{IssuedAt: time.Unix(r.CreatedAt, 0).UTC(), ID: model.DocumentID(Name, upstreamID(r))}, true
}

func upstreamID(r serviceapi.Record) string { return strconv.FormatInt(r.RecordID, 10) }
