package redact_test

import (
	"regexp"
	"sync"
	"testing"

	"docviewer/pkg/redact"
)

// A made-up policy: these tests know nothing about VINs or documents.
func newRedactor() *redact.Redactor {
	return redact.New(redact.Config{
		Rules: map[string]redact.Rule{
			"card":   {Action: redact.Mask, KeepLast: 4},
			"secret": {Action: redact.Mask},
			"body":   {Action: redact.Drop},
		},
		Patterns:        []*regexp.Regexp{regexp.MustCompile(`\b\d{3}-\d{4}\b`)}, // a phone-shaped token
		PatternKeepLast: 2,
	})
}

func TestField(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		want     string
		wantKeep bool
	}{
		{name: "mask_keeps_the_last_characters", key: "card", value: "4111222233334444", want: "************4444", wantKeep: true},
		{name: "mask_everything_when_keep_last_is_zero", key: "secret", value: "hunter2", want: "*******", wantKeep: true},
		{name: "value_no_longer_than_keep_last_is_masked_entirely", key: "card", value: "1234", want: "****", wantKeep: true},
		{name: "value_shorter_than_keep_last_is_masked_entirely", key: "card", value: "12", want: "**", wantKeep: true},
		{name: "empty_value_stays_empty", key: "card", value: "", want: "", wantKeep: true},
		{name: "mask_counts_runes_not_bytes", key: "card", value: "ÄÖÜ123456", want: "*****3456", wantKeep: true},
		{name: "drop_removes_the_field", key: "body", value: "anything", want: "", wantKeep: false},
		{name: "unknown_key_is_kept", key: "user", value: "alice", want: "alice", wantKeep: true},
		{name: "kept_values_are_still_scrubbed_of_patterns", key: "note", value: "call 555-1234 today", want: "call ******34 today", wantKeep: true},
		{name: "masked_values_are_not_scrubbed_twice", key: "card", value: "555-1234", want: "****1234", wantKeep: true},
	}
	r := newRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, keep := r.Field(tt.key, tt.value)
			if got != tt.want || keep != tt.wantKeep {
				t.Errorf("Field(%q, %q) = %q, %v; want %q, %v", tt.key, tt.value, got, keep, tt.want, tt.wantKeep)
			}
		})
	}
}

func TestText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no_match_is_unchanged", in: "nothing to see", want: "nothing to see"},
		{name: "one_match_is_masked_keeping_the_last_two", in: "call 555-1234", want: "call ******34"},
		{name: "every_match_is_masked", in: "555-1234 or 555-9876", want: "******34 or ******76"},
		{name: "word_boundaries_are_respected", in: "x555-1234y", want: "x555-1234y"},
		{name: "empty", in: "", want: ""},
	}
	r := newRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Text(tt.in); got != tt.want {
				t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRule(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want redact.Rule
	}{
		{name: "configured_key", key: "card", want: redact.Rule{Action: redact.Mask, KeepLast: 4}},
		{name: "unknown_key_is_keep", key: "user", want: redact.Rule{Action: redact.Keep}},
	}
	r := newRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Rule(tt.key); got != tt.want {
				t.Errorf("Rule(%q) = %+v, want %+v", tt.key, got, tt.want)
			}
		})
	}
}

func TestNoRulesNoPatterns(t *testing.T) {
	tests := []struct {
		name string
		key  string
		in   string
	}{
		{name: "field_passes_through", key: "anything", in: "555-1234"},
		{name: "text_passes_through", key: "", in: "555-1234"},
	}
	r := redact.New(redact.Config{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, keep := r.Field(tt.key, tt.in)
			if got != tt.in || !keep {
				t.Errorf("Field() = %q, %v; want unchanged and kept", got, keep)
			}
			if got := r.Text(tt.in); got != tt.in {
				t.Errorf("Text() = %q, want unchanged", got)
			}
		})
	}
}

// TestConcurrentUse: a Redactor is shared by every log line and span.
func TestConcurrentUse(t *testing.T) {
	r := newRedactor()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if got, _ := r.Field("card", "4111222233334444"); got != "************4444" {
				t.Errorf("Field() = %q", got)
			}
		})
	}
	wg.Wait()
}
