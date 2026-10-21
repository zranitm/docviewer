package mock

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Fault is what a mock does to every API call: wait, hang, fail some of the
// time, or fail every time with a fixed status. The zero value is no fault.
type Fault struct {
	LatencyMS int     `json:"latency_ms" mapstructure:"latency_ms"` // added before answering
	ErrorRate float64 `json:"error_rate" mapstructure:"error_rate"` // share of calls answered 503, 0 to 1
	Hang      bool    `json:"hang" mapstructure:"hang"`             // never answer; the caller's deadline decides
	Status    int     `json:"status" mapstructure:"status"`         // non-zero: every call gets this status
}

// Validate checks the ranges.
func (f Fault) Validate() error {
	switch {
	case f.LatencyMS < 0:
		return errors.New("latency_ms must not be negative")
	case f.ErrorRate < 0 || f.ErrorRate > 1:
		return errors.New("error_rate must be between 0 and 1")
	case f.Status != 0 && (f.Status < http.StatusBadRequest || f.Status > 599):
		return errors.New("status must be a 4xx or 5xx code, or 0 for none")
	}
	return nil
}

// Faults is the current fault, changed live through the admin endpoint.
type Faults struct {
	mu  sync.RWMutex
	cur Fault
}

// NewFaults starts with initial, the startup default from configuration.
func NewFaults(initial Fault) *Faults { return &Faults{cur: initial} }

// Current returns the fault in force.
func (f *Faults) Current() Fault {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cur
}

// Set replaces the fault in force; fields left out of the new one are off.
func (f *Faults) Set(fault Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cur = fault
}

// Reset removes every fault.
func (f *Faults) Reset() { f.Set(Fault{}) }

// Middleware applies the fault in force to each request before next sees it.
func (f *Faults) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := f.Current()
		if cur.Hang {
			<-r.Context().Done() // the caller gives up or the server stops
			return
		}
		if cur.LatencyMS > 0 {
			select {
			case <-time.After(time.Duration(cur.LatencyMS) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		if cur.Status != 0 {
			writeError(w, cur.Status, "injected fault")
			return
		}
		if cur.ErrorRate > 0 && rand.Float64() < cur.ErrorRate { //nolint:gosec // a fault injector, not a secret
			writeError(w, http.StatusServiceUnavailable, "injected fault")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdminHandler serves the fault controls: GET shows the fault in force, POST
// replaces it with the JSON body, DELETE removes it. Every success answers
// with the fault now in force.
func (f *Faults) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			var fault Fault
			if err := json.NewDecoder(r.Body).Decode(&fault); err != nil {
				writeError(w, http.StatusBadRequest, "body must be a JSON fault")
				return
			}
			if err := fault.Validate(); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			f.Set(fault)
		case http.MethodDelete:
			f.Reset()
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "use GET, POST or DELETE")
			return
		}
		writeJSON(w, http.StatusOK, f.Current())
	})
}
