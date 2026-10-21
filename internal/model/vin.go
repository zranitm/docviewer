package model

import (
	"errors"
	"fmt"
	"strings"
)

// VIN is a normalised vehicle identification number: 17 upper-case letters
// and digits. Only ParseVIN makes one.
type VIN string

const (
	vinLength    = 17
	vinForbidden = "IOQ" // excluded by the VIN standard: they look like 1 and 0
)

// ErrInvalidVIN is the domain's one error: the input isn't a VIN.
var ErrInvalidVIN = errors.New("invalid VIN")

// ParseVIN trims and upper-cases the input, then validates it:
// 17 characters, letters and digits only, never I, O or Q.
// The check digit is deliberately not enforced (PRD A-02).
func ParseVIN(raw string) (VIN, error) {
	rs := []rune(strings.ToUpper(strings.TrimSpace(raw)))
	if len(rs) != vinLength {
		return "", fmt.Errorf("%w: must be %d characters, got %d", ErrInvalidVIN, vinLength, len(rs))
	}
	for i, r := range rs {
		switch {
		case strings.ContainsRune(vinForbidden, r):
			return "", fmt.Errorf("%w: %q is not allowed (position %d)", ErrInvalidVIN, r, i+1)
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
		default:
			return "", fmt.Errorf("%w: invalid character %q (position %d)", ErrInvalidVIN, r, i+1)
		}
	}
	return VIN(string(rs)), nil
}
