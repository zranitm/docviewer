package redactslog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"reflect"
	"regexp"
	"testing"

	"docviewer/pkg/redact"
	"docviewer/pkg/redact/redactslog"
)

// A made-up policy with a phone-shaped pattern.
func newRedactor() *redact.Redactor {
	return redact.New(redact.Config{
		Rules: map[string]redact.Rule{
			"card": {Action: redact.Mask, KeepLast: 4},
			"body": {Action: redact.Drop},
		},
		Patterns:        []*regexp.Regexp{regexp.MustCompile(`\b\d{3}-\d{4}\b`)},
		PatternKeepLast: 2,
	})
}

// logged runs log and returns the single JSON record it wrote, without time.
func logged(t *testing.T, log func(l *slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	next := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})
	log(slog.New(redactslog.NewHandler(next, newRedactor())))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("record %q is not JSON: %v", buf.String(), err)
	}
	return rec
}

type valuer struct{ card string }

func (v valuer) LogValue() slog.Value { return slog.StringValue(v.card) }

func TestHandle(t *testing.T) {
	tests := []struct {
		name string
		log  func(l *slog.Logger)
		want map[string]any
	}{
		{
			name: "message_is_scrubbed_of_patterns",
			log:  func(l *slog.Logger) { l.Info("call 555-1234 back") },
			want: map[string]any{"level": "INFO", "msg": "call ******34 back"},
		},
		{
			name: "record_attribute_is_masked_by_key",
			log:  func(l *slog.Logger) { l.Info("pay", "card", "4111222233334444") },
			want: map[string]any{"level": "INFO", "msg": "pay", "card": "************4444"},
		},
		{
			name: "record_attribute_is_dropped_by_key",
			log:  func(l *slog.Logger) { l.Info("req", "body", "<xml>", "status", 200) },
			want: map[string]any{"level": "INFO", "msg": "req", "status": float64(200)},
		},
		{
			name: "with_attributes_are_redacted_too",
			log:  func(l *slog.Logger) { l.With("card", "4111222233334444", "body", "x").Info("pay") },
			want: map[string]any{"level": "INFO", "msg": "pay", "card": "************4444"},
		},
		{
			name: "attributes_inside_a_group_are_redacted_by_their_own_key",
			log:  func(l *slog.Logger) { l.Info("pay", slog.Group("payment", "card", "4111222233334444", "amount", 10)) },
			want: map[string]any{"level": "INFO", "msg": "pay", "payment": map[string]any{"card": "************4444", "amount": float64(10)}},
		},
		{
			name: "with_group_keeps_redacting_by_key",
			log:  func(l *slog.Logger) { l.WithGroup("payment").Info("pay", "card", "4111222233334444") },
			want: map[string]any{"level": "INFO", "msg": "pay", "payment": map[string]any{"card": "************4444"}},
		},
		{
			name: "log_valuer_is_resolved_before_redaction",
			log:  func(l *slog.Logger) { l.Info("pay", "card", valuer{card: "4111222233334444"}) },
			want: map[string]any{"level": "INFO", "msg": "pay", "card": "************4444"},
		},
		{
			name: "error_text_is_scrubbed",
			log: func(l *slog.Logger) {
				l.Error("upstream failed", "err", &url.Error{Op: "Get", URL: "http://sales/tel/555-1234", Err: errors.New("refused")})
			},
			want: map[string]any{"level": "ERROR", "msg": "upstream failed", "err": `Get "http://sales/tel/******34": refused`},
		},
		{
			name: "kept_string_values_are_scrubbed",
			log:  func(l *slog.Logger) { l.Info("note", "text", "ring 555-1234") },
			want: map[string]any{"level": "INFO", "msg": "note", "text": "ring ******34"},
		},
		{
			name: "scalars_pass_through_unchanged",
			log:  func(l *slog.Logger) { l.Info("n", "count", 5551234, "ok", true, "ratio", 1.5) },
			want: map[string]any{"level": "INFO", "msg": "n", "count": float64(5551234), "ok": true, "ratio": 1.5},
		},
		{
			name: "a_scalar_under_a_dropped_key_is_dropped",
			log:  func(l *slog.Logger) { l.Info("n", "body", 42) },
			want: map[string]any{"level": "INFO", "msg": "n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logged(t, tt.log); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("record = %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestEnabledDefersToNext(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		want  bool
	}{
		{name: "below_the_next_handlers_level", level: slog.LevelDebug, want: false},
		{name: "at_the_level", level: slog.LevelWarn, want: true},
	}
	next := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := redactslog.NewHandler(next, newRedactor())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.Enabled(context.Background(), tt.level); got != tt.want {
				t.Errorf("Enabled(%v) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}
