// Package mock is the test harness: the mocks' own response structs, an
// in-memory store with keyset paging, fault controls and fixtures. The
// service never imports it.
package mock

import (
	"cmp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Key is a record's place in newest-first order.
type Key struct {
	At time.Time
	ID string
}

// Store holds one mock system's records per VIN, sorted newest first:
// date descending, then ID descending, with IDs compared by cmpID
// (as strings for Sales, as numbers for Service).
type Store[T any] struct {
	byVIN map[string][]T
	key   func(T) Key
	cmpID func(a, b string) int
}

// NewStore makes an empty store whose records are placed by key and whose
// IDs are ordered by cmpID.
func NewStore[T any](key func(T) Key, cmpID func(a, b string) int) *Store[T] {
	return &Store[T]{byVIN: map[string][]T{}, key: key, cmpID: cmpID}
}

// Add appends records for vin and keeps its list newest first. A VIN added
// with no records is still known (Has), with an empty list.
func (s *Store[T]) Add(vin string, recs ...T) {
	list := slices.Concat(s.byVIN[vin], recs)
	slices.SortStableFunc(list, func(a, b T) int { return s.compare(s.key(b), s.key(a)) })
	s.byVIN[vin] = list
}

// Has reports whether the system knows vin at all; an unknown one is a 404.
func (s *Store[T]) Has(vin string) bool {
	_, ok := s.byVIN[vin]
	return ok
}

// Page returns up to limit records for vin that come after `after`
// (nil: from the newest), and whether more remain.
func (s *Store[T]) Page(vin string, after *Key, limit int) ([]T, bool) {
	recs := s.byVIN[vin]
	start := 0
	if after != nil {
		start = sort.Search(len(recs), func(i int) bool { return s.older(s.key(recs[i]), *after) })
	}
	end := min(start+limit, len(recs))
	return recs[start:end], end < len(recs)
}

// older reports whether k comes strictly after a in newest-first order.
func (s *Store[T]) older(k, a Key) bool { return s.compare(k, a) < 0 }

// compare orders keys oldest first: by time, then by ID with cmpID.
func (s *Store[T]) compare(a, b Key) int {
	if c := a.At.Compare(b.At); c != 0 {
		return c
	}
	return s.cmpID(a.ID, b.ID)
}

// CompareStringIDs orders IDs as text (Sales: "S-1002" after "S-1001").
func CompareStringIDs(a, b string) int { return strings.Compare(a, b) }

// CompareNumericIDs orders IDs as integers (Service: 10000 after 9999). A
// non-numeric ID sorts after every number, and as text among its kind.
func CompareNumericIDs(a, b string) int {
	ai, errA := strconv.ParseInt(a, 10, 64)
	bi, errB := strconv.ParseInt(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		return cmp.Compare(ai, bi)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}
