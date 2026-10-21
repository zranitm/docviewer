package aggregator_test

import (
	"errors"
	"testing"

	"docviewer/internal/aggregator"
)

func TestStatusString(t *testing.T) {
	tests := []struct {
		name   string
		status aggregator.Status
		want   string
	}{
		{name: "ok", status: aggregator.StatusOK, want: "OK"},
		{name: "timeout", status: aggregator.StatusTimeout, want: "Timeout"},
		{name: "error", status: aggregator.StatusError, want: "Error"},
		{name: "done", status: aggregator.StatusDone, want: "Done"},
		{name: "unavailable", status: aggregator.StatusUnavailable, want: "Unavailable"},
		{name: "zero_value_is_not_a_status", status: 0, want: "Status(0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPhaseValid(t *testing.T) {
	tests := []struct {
		name  string
		phase aggregator.Phase
		want  bool
	}{
		{name: "start", phase: aggregator.PhaseStart, want: true},
		{name: "after", phase: aggregator.PhaseAfter, want: true},
		{name: "done", phase: aggregator.PhaseDone, want: true},
		{name: "unavailable", phase: aggregator.PhaseUnavailable, want: true},
		{name: "empty_is_not_a_phase", phase: "", want: false},
		{name: "unknown_word", phase: "paused", want: false},
		{name: "wrong_case", phase: "Start", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.phase.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSourcesUnavailableError(t *testing.T) {
	tests := []struct {
		name          string
		statuses      []aggregator.Status
		wantRetryable bool
	}{
		{name: "timeout_is_retryable", statuses: []aggregator.Status{aggregator.StatusTimeout}, wantRetryable: true},
		{name: "error_is_retryable", statuses: []aggregator.Status{aggregator.StatusError}, wantRetryable: true},
		{name: "unavailable_only_is_not", statuses: []aggregator.Status{aggregator.StatusUnavailable, aggregator.StatusUnavailable}, wantRetryable: false},
		{name: "mixed_is_retryable", statuses: []aggregator.Status{aggregator.StatusUnavailable, aggregator.StatusTimeout}, wantRetryable: true},
		{name: "done_alone_is_not", statuses: []aggregator.Status{aggregator.StatusDone, aggregator.StatusUnavailable}, wantRetryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &aggregator.SourcesUnavailableError{}
			for _, s := range tt.statuses {
				e.Sources = append(e.Sources, aggregator.SourceResult{Status: s})
			}
			if got := e.Retryable(); got != tt.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", got, tt.wantRetryable)
			}
			if !errors.Is(e, aggregator.ErrSourcesUnavailable) {
				t.Error("the typed error does not unwrap to ErrSourcesUnavailable")
			}
			if e.Error() != aggregator.ErrSourcesUnavailable.Error() {
				t.Errorf("Error() = %q, want the sentinel's text", e.Error())
			}
		})
	}
}
