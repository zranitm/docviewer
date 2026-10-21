// Package model holds the domain: Document, Entry, SourceRef, VIN and source
// names. It has no JSON tags; the API and the clients own their wire shapes.
package model

import (
	"strings"
	"time"
)

// Source names an upstream system. Each adapter declares its own name, so
// adding a source never touches this package.
type Source string

// Document is one document as a source system holds it.
type Document struct {
	ID       string // "<source>:<upstream id>"
	Source   Source
	Type     string // e.g. "repair_order"; "other" if unknown
	Title    string
	Summary  string // only when the source provides one
	IssuedAt time.Time
	URL      string
	Checksum string // SHA-256 of the file; empty if the source gives none
}

// Entry is one row of the consolidated list. Sources has one entry today; it is
// a list so the planned de-duplication (HLD section 14) can add more.
type Entry struct {
	Type     string
	Title    string
	Summary  string
	IssuedAt time.Time
	Sources  []SourceRef
}

// SourceRef is one system's copy of an entry's file.
type SourceRef struct {
	Source Source
	ID     string
	URL    string
}

const idSep = ":"

// DocumentID is how a document is identified outside its source,
// "<source>:<upstream id>", so IDs never collide across sources.
func DocumentID(src Source, upstreamID string) string { return string(src) + idSep + upstreamID }

// UpstreamID strips the source prefix from a DocumentID.
func UpstreamID(src Source, id string) string { return strings.TrimPrefix(id, string(src)+idSep) }
