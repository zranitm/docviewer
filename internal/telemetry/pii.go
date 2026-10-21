// Package telemetry is the service's own telemetry: its metric instruments,
// its PII policy, and the setup that plugs the redaction adapters it chooses
// into pkg/otelsetup.
package telemetry

import (
	"regexp"

	"docviewer/pkg/redact"
)

// The PII policy (LLD section 8). Changing what counts as PII means editing
// this file, not the redaction code.
const vinKeepLast = 4 // enough to tell vehicles apart while debugging

// vinPattern is a VIN-shaped token anywhere in a message or string value:
// 17 characters, letters and digits, never I, O or Q. It may over-redact
// the odd 17-character word; that is the safe direction.
var vinPattern = regexp.MustCompile(`(?i)\b[A-HJ-NPR-Z0-9]{17}\b`)

func policy() redact.Config {
	mask := redact.Rule{Action: redact.Mask, KeepLast: vinKeepLast}
	drop := redact.Rule{Action: redact.Drop}
	return redact.Config{
		Rules: map[string]redact.Rule{
			"vin": mask,
			// Request logs carry the route template instead.
			"path": drop, "url": drop, "url.path": drop, "url.full": drop,
			// The cursor encodes the VIN.
			"query": drop, "url.query": drop, "cursor": drop,
			// Document fields and upstream bodies can hold customer names.
			"title": drop, "summary": drop, "notes": drop, "work_notes": drop, "body": drop,
		},
		Patterns:        []*regexp.Regexp{vinPattern},
		PatternKeepLast: vinKeepLast,
	}
}

// NewRedactor applies the PII policy.
func NewRedactor() *redact.Redactor { return redact.New(policy()) }
