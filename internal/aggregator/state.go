package aggregator

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// Query is everything that defines the list. New dimensions (an order, a
// filter) are added as fields with a default, and to the cursor's token.
type Query struct {
	VIN model.VIN
}

// request is the one place a Query turns into a source.Request: a new Query
// field is mapped here and nowhere else.
func (q Query) request(after *source.Position, limit int) source.Request {
	return source.Request{VIN: q.VIN, After: after, Limit: limit}
}

// Phase is how far a source has got. Its values are also the names the cursor
// writes, so they are defined once, here. The zero value is not a phase, so a
// missing entry can never pass as PhaseStart.
type Phase string

// The phases, as the cursor writes them (HLD section 7).
const (
	PhaseStart       Phase = "start"       // nothing returned yet: read from the beginning
	PhaseAfter       Phase = "after"       // At is the last document returned
	PhaseDone        Phase = "done"        // nothing left
	PhaseUnavailable Phase = "unavailable" // failed permanently; not called again
)

// Valid reports whether p is one of the phases above.
func (p Phase) Valid() bool {
	switch p {
	case PhaseStart, PhaseAfter, PhaseDone, PhaseUnavailable:
		return true
	}
	return false
}

// SourceState is where paging stands in one source.
type SourceState struct {
	Phase Phase
	At    *source.Position // set when Phase is PhaseAfter
}

// State is where paging stands: which list, and how far each source has got.
// The aggregator pages on it; cursor.go turns it into the token clients hold.
type State struct {
	Query   Query
	Sources map[model.Source]SourceState
}

// nothingShown reports whether no source has returned anything yet: every
// source is still at the start, as on a first page.
func (s State) nothingShown() bool {
	for _, ss := range s.Sources {
		if ss.Phase != PhaseStart {
			return false
		}
	}
	return true
}

// flightKey identifies identical requests. %+v includes every Query field, so
// new dimensions join the key without code changes.
func (s State) flightKey(limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%+v|%d", s.Query, limit)
	for _, name := range slices.Sorted(maps.Keys(s.Sources)) {
		ss := s.Sources[name]
		fmt.Fprintf(&b, "|%s:%s", name, ss.Phase)
		if ss.At != nil {
			fmt.Fprintf(&b, ":%s:%s", ss.At.IssuedAt.Format(time.RFC3339Nano), ss.At.ID)
		}
	}
	return b.String()
}
