package ghatui

import (
	"testing"
	"time"
)

func TestRollSeverity(t *testing.T) {
	tests := []struct {
		name string
		in   []State
		want State
	}{
		{"empty", nil, StateUnknown},
		{"all green", []State{StateSuccess, StateSuccess}, StateSuccess},
		// The deliberate ordering: a red PR stays red while a retried job spins.
		{"failure beats running", []State{StateRunning, StateFailure}, StateFailure},
		{"failure beats cancelled", []State{StateCancelled, StateFailure}, StateFailure},
		{"cancelled beats running", []State{StateRunning, StateCancelled}, StateCancelled},
		{"running beats pending", []State{StatePending, StateRunning}, StateRunning},
		{"pending beats neutral", []State{StateNeutral, StatePending}, StatePending},
		{"neutral beats skipped", []State{StateSkipped, StateNeutral}, StateNeutral},
		// A skip is a normal green outcome, so it must not grey out a parent
		// whose other checks all passed.
		{"success beats skipped", []State{StateSkipped, StateSuccess}, StateSuccess},
		{"all skipped stays skipped", []State{StateSkipped, StateSkipped}, StateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Roll(tt.in); got != tt.want {
				t.Errorf("Roll(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCheckState(t *testing.T) {
	tests := []struct {
		status, conclusion string
		want               State
	}{
		{"COMPLETED", "SUCCESS", StateSuccess},
		{"COMPLETED", "FAILURE", StateFailure},
		{"COMPLETED", "TIMED_OUT", StateFailure},
		{"COMPLETED", "STARTUP_FAILURE", StateFailure},
		{"COMPLETED", "CANCELLED", StateCancelled},
		{"COMPLETED", "SKIPPED", StateSkipped},
		{"COMPLETED", "NEUTRAL", StateNeutral},
		{"COMPLETED", "STALE", StateNeutral},
		{"COMPLETED", "ACTION_REQUIRED", StatePending},
		{"IN_PROGRESS", "", StateRunning},
		{"WAITING", "", StateRunning},
		{"QUEUED", "", StatePending},
		{"REQUESTED", "", StatePending},
		{"PENDING", "", StatePending},
		{"SOMETHING_NEW", "", StateUnknown},
	}
	for _, tt := range tests {
		if got := checkState(tt.status, tt.conclusion); got != tt.want {
			t.Errorf("checkState(%q,%q) = %v, want %v", tt.status, tt.conclusion, got, tt.want)
		}
	}
}

func TestStatusState(t *testing.T) {
	tests := map[string]State{
		"SUCCESS":  StateSuccess,
		"FAILURE":  StateFailure,
		"ERROR":    StateFailure,
		"PENDING":  StatePending,
		"EXPECTED": StatePending,
		"WAT":      StateUnknown,
	}
	for in, want := range tests {
		if got := statusState(in); got != want {
			t.Errorf("statusState(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStateTerminal(t *testing.T) {
	for _, s := range []State{StateSuccess, StateSkipped, StateNeutral, StateCancelled, StateFailure} {
		if !s.Terminal() {
			t.Errorf("%v should be terminal", s)
		}
	}
	for _, s := range []State{StatePending, StateRunning, StateUnknown} {
		if s.Terminal() {
			t.Errorf("%v should not be terminal", s)
		}
	}
}

func TestCheckDuration(t *testing.T) {
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(-5 * time.Minute)

	// Finished: the recorded span.
	done := Check{StartedAt: start, CompletedAt: start.Add(90 * time.Second)}
	if got := done.Duration(now); got != 90*time.Second {
		t.Errorf("finished duration = %v, want 90s", got)
	}
	// Running: up to now.
	if got := (Check{StartedAt: start}).Duration(now); got != 5*time.Minute {
		t.Errorf("running duration = %v, want 5m", got)
	}
	// Queued: no start time, so no duration.
	if got := (Check{}).Duration(now); got != 0 {
		t.Errorf("queued duration = %v, want 0", got)
	}
	// Clock skew must not produce a negative span.
	skew := Check{StartedAt: now, CompletedAt: now.Add(-time.Minute)}
	if got := skew.Duration(now); got != 0 {
		t.Errorf("skewed duration = %v, want 0", got)
	}
}

func TestPullRequestCounts(t *testing.T) {
	pr := PullRequest{Checks: []Check{
		{State: StateSuccess},
		{State: StateSkipped}, // skipped counts as passed: it is not blocking
		{State: StateFailure},
		{State: StateRunning},
	}}
	passed, total := pr.Counts()
	if passed != 2 || total != 4 {
		t.Errorf("Counts() = %d/%d, want 2/4", passed, total)
	}
	if pr.State() != StateFailure {
		t.Errorf("State() = %v, want failure", pr.State())
	}
}

func TestGroupByWorkflowOrdering(t *testing.T) {
	checks := []Check{
		{Name: "zeta", Workflow: "CI", State: StateSuccess},
		{Name: "alpha", Workflow: "CI", State: StateSuccess},
		{Name: "unit", Workflow: "Android", State: StateRunning},
		{Name: "lint", Workflow: "iOS Field", State: StateFailure},
		{Name: "orphan", Workflow: "", State: StateSuccess},
	}
	groups := groupByWorkflow(checks)
	if len(groups) != 4 {
		t.Fatalf("got %d groups, want 4", len(groups))
	}
	// Worst state first, then name: failure, running, then the two green ones
	// alphabetically.
	want := []string{"iOS Field", "Android", "CI", "other"}
	for i, name := range want {
		if groups[i].Name != name {
			t.Errorf("group %d = %q, want %q", i, groups[i].Name, name)
		}
	}
	// Checks within a workflow sort by name.
	ci := groups[2]
	if ci.Checks[0].Name != "alpha" || ci.Checks[1].Name != "zeta" {
		t.Errorf("CI checks = %q, %q; want alpha, zeta", ci.Checks[0].Name, ci.Checks[1].Name)
	}
	// A check with no workflow lands in "other".
	if groups[3].Checks[0].Name != "orphan" {
		t.Errorf("other group = %v", groups[3].Checks)
	}
}

func TestGroupCounts(t *testing.T) {
	g := workflowGroup{Checks: []Check{
		{State: StateSuccess}, {State: StateSkipped}, {State: StatePending},
	}}
	passed, total := g.counts()
	if passed != 2 || total != 3 {
		t.Errorf("counts() = %d/%d, want 2/3", passed, total)
	}
	if g.state() != StatePending {
		t.Errorf("state() = %v, want pending", g.state())
	}
}
