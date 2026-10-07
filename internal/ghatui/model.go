// Package ghatui models the open pull requests of a GitHub repository and the
// GitHub Actions checks running against them, as a tree the terminal UI in
// cmd/ghaview draws.
//
// Everything here is pure: fetching talks to the `gh` CLI through a Runner, and
// drawing writes strings. That split is what lets the interesting parts -- the
// state rollup, the tree shape, the key handling -- be tested without a network
// or a terminal.
package ghatui

import (
	"sort"
	"strings"
	"time"
)

// State is the single vocabulary the UI colours by. GitHub reports a check's
// progress in two fields (status, then conclusion once it finishes) and a
// commit status in one; both collapse into this.
type State int

// The states a check can be in, ordered from unknown through to failure. The
// rollup order a parent shows is severity(), not this declaration order.
const (
	// StateUnknown is a state GitHub reported that this does not model.
	StateUnknown State = iota
	// StateSuccess is a check that passed.
	StateSuccess
	// StateSkipped is a check that was skipped, usually by a path filter.
	StateSkipped
	// StateNeutral is a check that finished without passing or failing.
	StateNeutral
	// StatePending is a check that is queued but has not started.
	StatePending
	// StateRunning is a check that is in progress.
	StateRunning
	// StateCancelled is a check that was cancelled.
	StateCancelled
	// StateFailure is a check that failed.
	StateFailure
)

// String is the lowercase word shown in the UI.
func (s State) String() string {
	switch s {
	case StateSuccess:
		return "passed"
	case StateSkipped:
		return "skipped"
	case StateNeutral:
		return "neutral"
	case StatePending:
		return "queued"
	case StateRunning:
		return "running"
	case StateCancelled:
		return "cancelled"
	case StateFailure:
		return "failed"
	default:
		return "unknown"
	}
}

// Terminal reports whether the state can still change on its own.
func (s State) Terminal() bool {
	return s != StatePending && s != StateRunning && s != StateUnknown
}

// severity orders states for the rollup: the worst one a parent contains is the
// one the parent shows. Failure outranks running so a red PR stays red while a
// retried job spins, which is the thing a reviewer needs to see first.
//
// Skipped ranks below success deliberately. A workflow that skips most of its
// jobs on a path filter is a normal green result, and Counts treats a skip as
// passed; letting one skip grey out a fully passing pull request would
// contradict the "20/20" beside it.
func (s State) severity() int {
	switch s {
	case StateFailure:
		return 6
	case StateCancelled:
		return 5
	case StateRunning:
		return 4
	case StatePending:
		return 3
	case StateNeutral:
		return 2
	case StateSuccess:
		return 1
	case StateSkipped:
		return 0
	default:
		return -1
	}
}

// Roll returns the state a parent node shows for the children it contains.
func Roll(states []State) State {
	out := StateUnknown
	for _, s := range states {
		if s.severity() > out.severity() {
			out = s
		}
	}
	return out
}

// checkState maps a CheckRun's (status, conclusion) pair onto a State.
// GitHub leaves conclusion empty until status is COMPLETED.
func checkState(status, conclusion string) State {
	switch strings.ToUpper(conclusion) {
	case "SUCCESS":
		return StateSuccess
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE":
		return StateFailure
	case "CANCELLED":
		return StateCancelled
	case "SKIPPED":
		return StateSkipped
	case "NEUTRAL", "STALE":
		return StateNeutral
	case "ACTION_REQUIRED":
		// A workflow waiting on a human is not broken, but it is not going to
		// finish either; pending is the honest colour.
		return StatePending
	}
	switch strings.ToUpper(status) {
	case "IN_PROGRESS", "WAITING":
		return StateRunning
	case "QUEUED", "REQUESTED", "PENDING":
		return StatePending
	}
	return StateUnknown
}

// statusState maps a commit status context's state onto a State. These come
// from integrations outside Actions; the repo has none today, but a PR carrying
// one should still render rather than vanish.
func statusState(state string) State {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return StateSuccess
	case "FAILURE", "ERROR":
		return StateFailure
	case "PENDING":
		return StatePending
	case "EXPECTED":
		return StatePending
	}
	return StateUnknown
}

// A Check is one CheckRun or commit status on a pull request's head commit.
type Check struct {
	Name        string
	Workflow    string
	State       State
	StartedAt   time.Time
	CompletedAt time.Time
	URL         string
}

// Duration is how long the check ran, or has been running. It is zero when
// GitHub has not reported a start time (a queued job).
func (c Check) Duration(now time.Time) time.Duration {
	if c.StartedAt.IsZero() {
		return 0
	}
	end := c.CompletedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(c.StartedAt) {
		return 0
	}
	return end.Sub(c.StartedAt)
}

// A PullRequest is one open PR and the checks on its head commit.
type PullRequest struct {
	Number         int
	Title          string
	Author         string
	Branch         string
	URL            string
	Draft          bool
	ReviewDecision string
	Mergeable      string
	UpdatedAt      time.Time
	HeadSHA        string
	Checks         []Check
}

// State is the worst state among the PR's checks.
func (p PullRequest) State() State {
	states := make([]State, 0, len(p.Checks))
	for _, c := range p.Checks {
		states = append(states, c.State)
	}
	return Roll(states)
}

// Counts returns how many checks have passed and how many exist, which is the
// "5/7" summary on the PR row.
func (p PullRequest) Counts() (passed, total int) {
	for _, c := range p.Checks {
		total++
		if c.State == StateSuccess || c.State == StateSkipped {
			passed++
		}
	}
	return passed, total
}

// A Branch is a branch's head commit and the checks on it. The tree shows the
// repository's default branch, which answers the question a red pull request
// always raises: is main itself broken, or is this change?
type Branch struct {
	Name      string
	SHA       string
	Headline  string
	URL       string
	Committed time.Time
	Checks    []Check
}

// State is the worst state among the branch's checks.
func (b Branch) State() State {
	states := make([]State, 0, len(b.Checks))
	for _, c := range b.Checks {
		states = append(states, c.State)
	}
	return Roll(states)
}

// Counts returns how many checks passed and how many exist.
func (b Branch) Counts() (passed, total int) {
	for _, c := range b.Checks {
		total++
		if c.State == StateSuccess || c.State == StateSkipped {
			passed++
		}
	}
	return passed, total
}

// ShortSHA is the abbreviated commit hash shown on the branch row.
func (b Branch) ShortSHA() string {
	if len(b.SHA) > 7 {
		return b.SHA[:7]
	}
	return b.SHA
}

// workflowGroup is one workflow's checks within a pull request.
type workflowGroup struct {
	Name   string
	Checks []Check
}

// groupByWorkflow buckets a PR's checks by workflow, preserving a stable order:
// workflows sorted by worst state first so a failure is at the top, then by
// name; checks within a workflow by name.
func groupByWorkflow(checks []Check) []workflowGroup {
	byName := map[string][]Check{}
	order := []string{}
	for _, c := range checks {
		name := c.Workflow
		if name == "" {
			name = "other"
		}
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = append(byName[name], c)
	}

	groups := make([]workflowGroup, 0, len(order))
	for _, name := range order {
		runs := byName[name]
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })
		groups = append(groups, workflowGroup{Name: name, Checks: runs})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i].state(), groups[j].state()
		if a != b {
			return a.severity() > b.severity()
		}
		return groups[i].Name < groups[j].Name
	})
	return groups
}

func (g workflowGroup) state() State {
	states := make([]State, 0, len(g.Checks))
	for _, c := range g.Checks {
		states = append(states, c.State)
	}
	return Roll(states)
}

func (g workflowGroup) counts() (passed, total int) {
	for _, c := range g.Checks {
		total++
		if c.State == StateSuccess || c.State == StateSkipped {
			passed++
		}
	}
	return passed, total
}
