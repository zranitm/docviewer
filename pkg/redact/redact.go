// Package redact is rule-driven redaction of key/value fields and free
// text. It imports no logging or tracing library and knows nothing about
// VINs or documents: the rules are configuration. Using it on logs or traces
// is the caller's choice, through the opt-in adapters redactslog and
// redactotel.
package redact

import (
	"regexp"
	"strings"
)

// Action is what happens to a field.
type Action int

// The actions; the zero value keeps the field.
const (
	Keep Action = iota
	Drop        // remove the field
	Mask        // replace the value with asterisks, leaving its last KeepLast characters
)

// Rule is the action for one field key.
type Rule struct {
	Action   Action
	KeepLast int // for Mask: trailing characters left visible; 0 masks everything
}

// Config is the policy a Redactor applies.
type Config struct {
	Rules           map[string]Rule  // by field key
	Patterns        []*regexp.Regexp // matches in free text and string values are masked
	PatternKeepLast int              // trailing characters left visible in each match
}

// Redactor applies a Config. It is safe for concurrent use.
type Redactor struct{ cfg Config }

// New makes a Redactor for cfg.
func New(cfg Config) *Redactor { return &Redactor{cfg: cfg} }

// Rule returns the rule for key, or Keep.
func (r *Redactor) Rule(key string) Rule { return r.cfg.Rules[key] }

// Field redacts one value: it returns the new value, and keep=false when the
// field must be dropped. Kept values are still scrubbed of pattern matches.
// Keys never change, so callers never track renamed fields.
func (r *Redactor) Field(key, value string) (string, bool) {
	switch rule := r.Rule(key); rule.Action {
	case Drop:
		return "", false
	case Mask:
		return mask(value, rule.KeepLast), true
	case Keep:
	}
	return r.Text(value), true
}

// Text masks every pattern match in free text, such as a log message.
func (r *Redactor) Text(s string) string {
	for _, p := range r.cfg.Patterns {
		s = p.ReplaceAllStringFunc(s, func(m string) string { return mask(m, r.cfg.PatternKeepLast) })
	}
	return s
}

const maskRune = "*"

// mask replaces all but the last keep runes with '*'. A value no longer than
// keep is masked entirely, so a short value is never shown in full.
func mask(s string, keep int) string {
	rs := []rune(s)
	if keep >= len(rs) {
		keep = 0
	}
	n := len(rs) - keep
	return strings.Repeat(maskRune, n) + string(rs[n:])
}
