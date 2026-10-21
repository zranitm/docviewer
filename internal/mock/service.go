package mock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// The mock Service System API (HLD section 5): snake_case, epoch seconds,
// integer IDs, GET /v2/records?vin=&limit=&before_ts=&before_id=.
const (
	serviceRecordsPattern = "GET /v2/records"
	serviceParamVIN       = "vin"
	serviceParamBeforeTS  = "before_ts"
	serviceParamBeforeID  = "before_id"
)

// ServiceRecord is one record as the Service mock sends it. In the fixture
// file download_link is absent; it is built from public_base_url on load.
type ServiceRecord struct {
	RecordID     int64  `json:"record_id"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	CreatedAt    int64  `json:"created_at"` // epoch seconds
	DownloadLink string `json:"download_link"`
	WorkNotes    string `json:"work_notes,omitempty"`
	FileSHA256   string `json:"file_sha256,omitempty"`
}

// ServicePage is the Service mock's response envelope.
type ServicePage struct {
	HasMore bool            `json:"has_more"`
	Records []ServiceRecord `json:"records"`
}

// serviceVehicle is one entry of fixtures/service.json.
type serviceVehicle struct {
	VIN     string          `json:"vin"`
	Records []ServiceRecord `json:"records"`
}

// ServiceKey places a record: its timestamp, then its ID as a number.
func ServiceKey(r ServiceRecord) Key {
	return Key{At: time.Unix(r.CreatedAt, 0).UTC(), ID: strconv.FormatInt(r.RecordID, 10)}
}

// LoadService reads the fixture and builds each record's download link from
// publicBaseURL.
func LoadService(r io.Reader, publicBaseURL string) (*Store[ServiceRecord], error) {
	var vehicles []serviceVehicle
	if err := json.NewDecoder(r).Decode(&vehicles); err != nil {
		return nil, fmt.Errorf("service fixture: %w", err)
	}
	s := NewStore(ServiceKey, CompareNumericIDs)
	for _, v := range vehicles {
		for i := range v.Records {
			rec := &v.Records[i]
			rec.DownloadLink = fileURL(publicBaseURL, strconv.FormatInt(rec.RecordID, 10))
		}
		s.Add(v.VIN, v.Records...)
	}
	return s, nil
}

// NewServiceHandler serves the Service mock: its records API behind the
// fault controls, the placeholder files, the admin endpoint and a health check.
func NewServiceHandler(store *Store[ServiceRecord], faults *Faults, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(serviceRecordsPattern, faults.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		vin := q.Get(serviceParamVIN)
		if vin == "" {
			writeError(w, http.StatusBadRequest, "vin is required")
			return
		}
		limit, err := parseLimit(q.Get(paramLimit))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		after, err := parseServiceBefore(q.Get(serviceParamBeforeTS), q.Get(serviceParamBeforeID))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !store.Has(vin) {
			writeError(w, http.StatusNotFound, "vehicle not found")
			return
		}
		recs, more := store.Page(vin, after, limit)
		writeJSON(w, http.StatusOK, ServicePage{HasMore: more, Records: recs})
	})))
	commonRoutes(mux, faults)
	return logging(log, cors(mux))
}

// parseServiceBefore reads before_ts and before_id, which come together or
// not at all; both empty means from the newest.
func parseServiceBefore(rawTS, rawID string) (*Key, error) {
	if rawTS == "" && rawID == "" {
		return nil, nil
	}
	if rawTS == "" || rawID == "" {
		return nil, errors.New("before_ts and before_id must be given together")
	}
	ts, err := strconv.ParseInt(rawTS, 10, 64)
	if err != nil {
		return nil, errors.New("before_ts must be epoch seconds")
	}
	if _, err := strconv.ParseInt(rawID, 10, 64); err != nil {
		return nil, errors.New("before_id must be an integer")
	}
	return &Key{At: time.Unix(ts, 0).UTC(), ID: rawID}, nil
}
