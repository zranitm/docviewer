package sales

import (
	"fmt"
	"strings"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/client/salesapi"
	"docviewer/internal/model"
)

// toPage maps a wire page to the domain (LLD section 6, Sales mapping),
// skipping malformed items. A page for another vehicle is permanent: the
// same request would get the same wrong answer.
func toPage(vin model.VIN, resp salesapi.DocumentPage) (source.Page, error) {
	if !strings.EqualFold(resp.VehicleVIN, string(vin)) {
		return source.Page{}, fmt.Errorf("%w: response is for another vehicle", source.ErrPermanent)
	}
	page := source.Page{HasMore: resp.HasMore}
	var last *source.Position // the last item that can at least be placed
	for _, d := range resp.Documents {
		doc, ok := toDocument(d)
		if ok {
			page.Docs = append(page.Docs, doc)
			continue
		}
		if pos, ok := position(d); ok {
			last = &pos
		}
	}
	if len(page.Docs) == 0 && len(resp.Documents) > 0 {
		page.SkipTo = last // step past a fully malformed page instead of asking for it forever
	}
	return page, nil
}

// toDocument maps one wire document. ok is false for a malformed item: one
// missing its ID, its date or its URL (HLD H-03).
func toDocument(d salesapi.Document) (model.Document, bool) {
	issuedAt, err := time.Parse(time.DateOnly, d.CreatedDate)
	if d.DocID == "" || err != nil || d.FileURL == "" {
		return model.Document{}, false
	}
	return model.Document{
		ID:       model.DocumentID(Name, d.DocID),
		Source:   Name,
		Type:     model.CanonicalType(d.DocType),
		Title:    d.Title,
		Summary:  strings.TrimSpace(d.Notes),
		IssuedAt: issuedAt,
		URL:      d.FileURL,
		Checksum: model.Checksum(d.FileSHA256),
	}, true
}

// position is where d sits in the Sales order, if it has an ID and a date.
func position(d salesapi.Document) (source.Position, bool) {
	issuedAt, err := time.Parse(time.DateOnly, d.CreatedDate)
	if d.DocID == "" || err != nil {
		return source.Position{}, false
	}
	return source.Position{IssuedAt: issuedAt, ID: model.DocumentID(Name, d.DocID)}, true
}
