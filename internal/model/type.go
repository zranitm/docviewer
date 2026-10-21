package model

import "strings"

// TypeOther is the document type for anything a source names that the
// viewer doesn't know.
const TypeOther = "other"

// knownTypes are the document types the viewer recognises across sources,
// in the canonical snake_case form (PRD section 5 examples).
var knownTypes = map[string]struct{}{
	"purchase_agreement":      {},
	"sales_invoice":           {},
	"finance_contract":        {},
	"registration":            {},
	"pre_delivery_inspection": {},
	"repair_order":            {},
	"service_invoice":         {},
	"inspection_report":       {},
	"recall_notice":           {},
}

// CanonicalType normalises an upstream type name (PURCHASE_AGREEMENT,
// repair_order) to its canonical form; anything unknown is TypeOther.
func CanonicalType(upstream string) string {
	t := strings.ToLower(strings.TrimSpace(upstream))
	if _, ok := knownTypes[t]; ok {
		return t
	}
	return TypeOther
}

const sha256HexLen = 64

// Checksum normalises an upstream SHA-256 to lower-case hex; anything that
// isn't a 64-character hex digest is dropped, since it can't be compared.
func Checksum(upstream string) string {
	s := strings.ToLower(strings.TrimSpace(upstream))
	if len(s) != sha256HexLen {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return s
}
