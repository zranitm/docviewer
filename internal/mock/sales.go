package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// The mock Sales System API (HLD section 5): camelCase, ISO dates, string
// IDs, GET /api/v1/vehicles/{vin}/documents?limit=&before=<date>|<id>.
const (
	salesDocumentsPattern = "GET /api/v1/vehicles/{vin}/documents"
	salesParamBefore      = "before"
	salesBeforeSep        = "|"
)

// SalesDocument is one document as the Sales mock sends it. In the fixture
// file fileUrl is absent; it is built from public_base_url on load.
type SalesDocument struct {
	DocID       string `json:"docId"`
	DocType     string `json:"docType"`
	Title       string `json:"title"`
	CreatedDate string `json:"createdDate"` // YYYY-MM-DD
	FileURL     string `json:"fileUrl"`
	Notes       string `json:"notes,omitempty"`
	FileSHA256  string `json:"fileSha256,omitempty"`
}

// SalesPage is the Sales mock's response envelope.
type SalesPage struct {
	VehicleVIN string          `json:"vehicleVin"`
	HasMore    bool            `json:"hasMore"`
	Documents  []SalesDocument `json:"documents"`
}

// salesVehicle is one entry of fixtures/sales.json.
type salesVehicle struct {
	VIN       string          `json:"vin"`
	Documents []SalesDocument `json:"documents"`
}

// SalesKey places a document: its date, then its ID as text. LoadSales
// rejects a date that doesn't parse, so a stored document always has one.
func SalesKey(d SalesDocument) Key {
	t, _ := time.Parse(time.DateOnly, d.CreatedDate)
	return Key{At: t, ID: d.DocID}
}

// LoadSales reads the fixture and builds each document's file link from
// publicBaseURL.
func LoadSales(r io.Reader, publicBaseURL string) (*Store[SalesDocument], error) {
	var vehicles []salesVehicle
	if err := json.NewDecoder(r).Decode(&vehicles); err != nil {
		return nil, fmt.Errorf("sales fixture: %w", err)
	}
	s := NewStore(SalesKey, CompareStringIDs)
	for _, v := range vehicles {
		for i := range v.Documents {
			d := &v.Documents[i]
			if _, err := time.Parse(time.DateOnly, d.CreatedDate); err != nil {
				return nil, fmt.Errorf("sales fixture: document %s: %w", d.DocID, err)
			}
			d.FileURL = fileURL(publicBaseURL, d.DocID)
		}
		s.Add(v.VIN, v.Documents...)
	}
	return s, nil
}

// NewSalesHandler serves the Sales mock: its documents API behind the fault
// controls, the placeholder files, the admin endpoint and a health check.
func NewSalesHandler(store *Store[SalesDocument], faults *Faults, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(salesDocumentsPattern, faults.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vin := r.PathValue("vin")
		q := r.URL.Query()
		limit, err := parseLimit(q.Get(paramLimit))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		after, err := parseSalesBefore(q.Get(salesParamBefore))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !store.Has(vin) {
			writeError(w, http.StatusNotFound, "vehicle not found")
			return
		}
		docs, more := store.Page(vin, after, limit)
		writeJSON(w, http.StatusOK, SalesPage{VehicleVIN: vin, HasMore: more, Documents: docs})
	})))
	commonRoutes(mux, faults)
	return logging(log, cors(mux))
}

// parseSalesBefore reads before=<YYYY-MM-DD>|<docId>; empty means from the newest.
func parseSalesBefore(raw string) (*Key, error) {
	if raw == "" {
		return nil, nil
	}
	date, id, ok := strings.Cut(raw, salesBeforeSep)
	if !ok || id == "" {
		return nil, fmt.Errorf("before must be <date>%s<docId>", salesBeforeSep)
	}
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil, fmt.Errorf("before: date must be YYYY-MM-DD")
	}
	return &Key{At: t, ID: id}, nil
}
