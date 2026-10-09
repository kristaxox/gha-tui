package ghatui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// appPRs covers the filter cases: a failing PR, a running one, a clean one by
// another author, and a draft.
func appPRs() []PullRequest {
	start := testNow.Add(-5 * time.Minute)
	return []PullRequest{
		{
			Number: 1, Title: "failing", Author: "alice", Mergeable: "MERGEABLE",
			URL: "https://example.test/pr/1", UpdatedAt: testNow,
			Checks: []Check{
				{Name: "build", Workflow: "CI", State: StateSuccess, StartedAt: start, CompletedAt: start.Add(time.Minute)},
				{Name: "test", Workflow: "CI", State: StateFailure, StartedAt: start, CompletedAt: start.Add(time.Minute)},
			},
		},
		{
			Number: 2, Title: "running", Author: "bob", Mergeable: "MERGEABLE",
			URL: "https://example.test/pr/2", UpdatedAt: testNow,
			Checks: []Check{{Name: "lint", Workflow: "CI", State: StateRunning, StartedAt: start}},
		},
		{
			Number: 3, Title: "clean", Author: "bob", Mergeable: "MERGEABLE",
			URL: "https://example.test/pr/3", UpdatedAt: testNow,
			Checks: []Check{{Name: "unit", Workflow: "CI", State: StateSuccess, StartedAt: start, CompletedAt: start.Add(time.Minute)}},
		},
		{
			Number: 4, Title: "draft", Author: "alice", Draft: true, Mergeable: "CONFLICTING",
			URL: "https://example.test/pr/4", UpdatedAt: testNow,
			Checks: []Check{{Name: "unit", Workflow: "CI", State: StateSuccess}},
		},
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	a := NewApp("exampleorg/widget")
	a.Viewer = "alice"
	a.SetData(appPRs(), nil, testNow)
	return a
}

// prNumbers lists the pull requests currently visible in the tree.
func prNumbers(a *App) []int {
	var out []int
	for _, r := range a.Rows() {
		if r.Node.Kind == NodePR && r.Node.PR != nil {
			out = append(out, r.Node.PR.Number)
		}
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findRow returns the index of the row with the given node key.
func findRow(a *App, key string) int {
	for i, r := range a.Rows() {
		if r.Node.Key == key {
			return i
		}
	}
	return -1
}

func TestDefaultFoldRule(t *testing.T) {
	a := NewApp("exampleorg/widget")
	a.SetData([]PullRequest{
		{Number: 1, Title: "x", Author: "alice", Checks: []Check{
			{Name: "green", Workflow: "Passing", State: StateSuccess},
		}},
		{Number: 2, Title: "y", Author: "alice", Checks: []Check{
			{Name: "red", Workflow: "Failing", State: StateFailure},
		}},
		{Number: 3, Title: "z", Author: "alice", Checks: []Check{
			{Name: "spin", Workflow: "Running", State: StateRunning},
		}},
		{Number: 4, Title: "w", Author: "alice", Checks: []Check{
			{Name: "stop", Workflow: "Cancelled", State: StateCancelled},
		}},
	}, nil, testNow)

	// The repo and its PRs open; a workflow opens only when it needs reading.
	cases := map[string]bool{
		"repo":              true,
		"pr:1":              true,
		"pr:1/wf:Passing":   false,
		"pr:2/wf:Failing":   true,
		"pr:3/wf:Running":   true,
		"pr:4/wf:Cancelled": true,
	}
	for key, want := range cases {
		if got := findRow(a, key) >= 0; !got {
			t.Fatalf("row %q is missing from the tree", key)
		}
		row := a.Rows()[findRow(a, key)]
		if row.Expanded != want {
			t.Errorf("%q expanded = %v, want %v", key, row.Expanded, want)
		}
	}
}

func TestFailingWorkflowUnfoldsItselfOnRefresh(t *testing.T) {
	a := NewApp("exampleorg/widget")
	green := []PullRequest{{Number: 1, Title: "x", Author: "alice", Checks: []Check{
		{Name: "job", Workflow: "CI", State: StateSuccess},
	}}}
	a.SetData(green, nil, testNow)
	if a.Rows()[findRow(a, "pr:1/wf:CI")].Expanded {
		t.Fatal("a passing workflow should start folded")
	}

	// The same workflow starts failing: the default is computed, so it opens.
	red := []PullRequest{{Number: 1, Title: "x", Author: "alice", Checks: []Check{
		{Name: "job", Workflow: "CI", State: StateFailure},
	}}}
	a.SetData(red, nil, testNow)
	if !a.Rows()[findRow(a, "pr:1/wf:CI")].Expanded {
		t.Error("a workflow that starts failing should unfold itself")
	}
}

func TestExplicitFoldSurvivesRefresh(t *testing.T) {
	a := newTestApp(t)
	key := "pr:1/wf:CI" // failing, so open by default
	a.sel = findRow(a, key)
	if !a.Rows()[a.sel].Expanded {
		t.Fatal("failing workflow should start open")
	}
	a.Key(" ", testNow) // fold it deliberately
	if a.Rows()[findRow(a, key)].Expanded {
		t.Fatal("space should have folded it")
	}

	a.SetData(appPRs(), nil, testNow) // a refresh must not undo the user's choice
	if a.Rows()[findRow(a, key)].Expanded {
		t.Error("an explicit fold should survive a refresh")
	}
}

func TestRefreshKeepsCursorOnNestedCheck(t *testing.T) {
	a := newTestApp(t)
	key := "pr:1/wf:CI/check:test"
	idx := findRow(a, key)
	if idx < 0 {
		t.Fatalf("check row missing; rows = %v", rowLabels(a.Rows()))
	}
	a.sel = idx

	// A refresh lands while the cursor sits on a nested check.
	a.SetData(appPRs(), nil, testNow.Add(time.Minute))
	if got := a.Selected(); got == nil || got.Key != key {
		t.Fatalf("cursor moved to %v, want %q", got, key)
	}

	// So does a spinner tick.
	a.Tick(testNow.Add(2 * time.Minute))
	if got := a.Selected(); got == nil || got.Key != key {
		t.Errorf("tick moved the cursor to %v, want %q", got, key)
	}
}

func TestCursorClampsWhenSelectionDisappears(t *testing.T) {
	a := newTestApp(t)
	a.sel = len(a.Rows()) - 1
	a.SetData(appPRs()[:1], nil, testNow) // the selected node is gone
	if a.SelectedIndex() >= len(a.Rows()) {
		t.Fatalf("cursor %d is past the end (%d rows)", a.SelectedIndex(), len(a.Rows()))
	}
	if a.Selected() == nil {
		t.Error("cursor should still land on a row")
	}
}

func TestFilters(t *testing.T) {
	a := newTestApp(t)
	if got := prNumbers(a); !equalInts(got, []int{1, 2, 3, 4}) {
		t.Fatalf("all = %v", got)
	}

	a.Key("f", testNow) // needs attention: failing or conflicting
	if got := prNumbers(a); !equalInts(got, []int{1, 4}) {
		t.Errorf("needs attention = %v, want [1 4]", got)
	}
	a.Key("f", testNow) // in flight
	if got := prNumbers(a); !equalInts(got, []int{2}) {
		t.Errorf("in flight = %v, want [2]", got)
	}
	a.Key("f", testNow) // mine (viewer is alice)
	if got := prNumbers(a); !equalInts(got, []int{1, 4}) {
		t.Errorf("mine = %v, want [1 4]", got)
	}
	a.Key("f", testNow) // back to all
	if got := prNumbers(a); !equalInts(got, []int{1, 2, 3, 4}) {
		t.Errorf("wrapped filter = %v", got)
	}
}

func TestMineFilterWithoutViewerShowsNothing(t *testing.T) {
	a := NewApp("exampleorg/widget")
	a.SetData(appPRs(), nil, testNow)
	a.filter = FilterMine
	a.rebuild(testNow)
	if got := prNumbers(a); len(got) != 0 {
		t.Errorf("mine with no viewer = %v, want none", got)
	}
}

func TestDraftToggle(t *testing.T) {
	a := newTestApp(t)
	a.Key("d", testNow)
	if got := prNumbers(a); !equalInts(got, []int{1, 2, 3}) {
		t.Errorf("drafts hidden = %v, want [1 2 3]", got)
	}
	a.Key("d", testNow)
	if got := prNumbers(a); !equalInts(got, []int{1, 2, 3, 4}) {
		t.Errorf("drafts shown = %v", got)
	}
}

func TestNavigationKeys(t *testing.T) {
	a := newTestApp(t)
	a.Key("G", testNow)
	if a.SelectedIndex() != len(a.Rows())-1 {
		t.Errorf("G = %d, want last row", a.SelectedIndex())
	}
	a.Key("g", testNow)
	if a.SelectedIndex() != 0 {
		t.Errorf("g = %d, want 0", a.SelectedIndex())
	}
	a.Key("j", testNow)
	if a.SelectedIndex() != 1 {
		t.Errorf("j = %d, want 1", a.SelectedIndex())
	}
	a.Key("k", testNow)
	if a.SelectedIndex() != 0 {
		t.Errorf("k = %d, want 0", a.SelectedIndex())
	}
	// Moving up from the top stays put rather than wrapping.
	a.Key("up", testNow)
	if a.SelectedIndex() != 0 {
		t.Errorf("up at the top = %d, want 0", a.SelectedIndex())
	}
	// And down from the bottom.
	a.Key("end", testNow)
	last := a.SelectedIndex()
	a.Key("down", testNow)
	if a.SelectedIndex() != last {
		t.Errorf("down at the bottom = %d, want %d", a.SelectedIndex(), last)
	}
}

func TestJumpBetweenPRs(t *testing.T) {
	a := newTestApp(t)
	a.sel = 0 // the repo row
	a.Key("J", testNow)
	if n := a.Selected(); n == nil || n.Kind != NodePR || n.PR.Number != 1 {
		t.Fatalf("J landed on %v", n)
	}
	a.Key("J", testNow)
	if n := a.Selected(); n == nil || n.PR.Number != 2 {
		t.Fatalf("second J landed on %v", n)
	}
	a.Key("K", testNow)
	if n := a.Selected(); n == nil || n.PR.Number != 1 {
		t.Fatalf("K landed on %v", n)
	}
}

func TestFoldAndAscend(t *testing.T) {
	a := newTestApp(t)
	check := findRow(a, "pr:1/wf:CI/check:test")
	a.sel = check
	// h on a leaf moves to its parent.
	a.Key("h", testNow)
	if n := a.Selected(); n == nil || n.Key != "pr:1/wf:CI" {
		t.Fatalf("h from a check landed on %v, want the workflow", n)
	}
	// h on an open node folds it.
	a.Key("h", testNow)
	if a.Rows()[findRow(a, "pr:1/wf:CI")].Expanded {
		t.Error("h should have folded the workflow")
	}
	// h again ascends to the pull request.
	a.Key("h", testNow)
	if n := a.Selected(); n == nil || n.Key != "pr:1" {
		t.Fatalf("h from a folded workflow landed on %v", n)
	}
	// l unfolds it again.
	a.sel = findRow(a, "pr:1/wf:CI")
	a.Key("l", testNow)
	if !a.Rows()[findRow(a, "pr:1/wf:CI")].Expanded {
		t.Error("l should have unfolded the workflow")
	}
}

func TestExpandAllAndCollapseAll(t *testing.T) {
	a := newTestApp(t)
	a.Key("e", testNow)
	for _, r := range a.Rows() {
		if len(r.Node.Children) > 0 && !r.Expanded {
			t.Errorf("e left %q folded", r.Node.Key)
		}
	}
	expanded := len(a.Rows())

	a.Key("c", testNow)
	if len(a.Rows()) >= expanded {
		t.Errorf("c did not reduce the row count (%d -> %d)", expanded, len(a.Rows()))
	}
	// The repo itself stays open, so the pull requests remain visible.
	if len(a.Rows()) != 5 {
		t.Errorf("after c: %d rows, want the repo and its 4 PRs: %v", len(a.Rows()), rowLabels(a.Rows()))
	}
}

func TestKeyActions(t *testing.T) {
	a := newTestApp(t)
	for _, key := range []string{"q", "ctrl-c", "esc"} {
		if got := a.Key(key, testNow); got != ActionQuit {
			t.Errorf("%q = %v, want quit", key, got)
		}
	}
	if got := a.Key("r", testNow); got != ActionRefresh {
		t.Errorf("r = %v, want refresh", got)
	}
	if got := a.Key("o", testNow); got != ActionOpen {
		t.Errorf("o = %v, want open", got)
	}
	if got := a.Key("j", testNow); got != ActionNone {
		t.Errorf("j = %v, want none", got)
	}
}

func TestHelpOverlayClosesOnAnyKey(t *testing.T) {
	a := newTestApp(t)
	a.Key("?", testNow)
	if !a.showHelp {
		t.Fatal("? should open help")
	}
	a.Key("j", testNow)
	if a.showHelp {
		t.Error("a keypress should close help")
	}
	// q still leaves from inside help.
	a.Key("?", testNow)
	if got := a.Key("q", testNow); got != ActionQuit {
		t.Errorf("q inside help = %v, want quit", got)
	}
}

func TestOpenURLFallsBackToPullRequest(t *testing.T) {
	a := newTestApp(t)
	// A check with a URL of its own.
	a.sel = findRow(a, "pr:1/wf:CI/check:test")
	a.rows[a.sel].Node.URL = "https://example.test/run/9"
	if got := a.OpenURL(); got != "https://example.test/run/9" {
		t.Errorf("check URL = %q", got)
	}
	// A workflow row has none, so it falls back to the pull request.
	a.sel = findRow(a, "pr:1/wf:CI")
	if got := a.OpenURL(); got != "https://example.test/pr/1" {
		t.Errorf("fallback URL = %q, want the pull request", got)
	}
	// The repo row has neither.
	a.sel = 0
	if got := a.OpenURL(); got != "" {
		t.Errorf("repo URL = %q, want empty", got)
	}
}

func TestSetErrorKeepsTree(t *testing.T) {
	a := newTestApp(t)
	before := len(a.Rows())
	a.SetError(errors.New("network is down"))
	if len(a.Rows()) != before {
		t.Errorf("rows changed after a failed refresh: %d -> %d", before, len(a.Rows()))
	}
	line := a.statusLine(Theme{}, 80, testNow)
	if !strings.Contains(line, "network is down") {
		t.Errorf("status line = %q, want the error", line)
	}
	// The next good refresh clears it.
	a.SetData(appPRs(), nil, testNow)
	if a.lastErr != nil {
		t.Error("a successful refresh should clear the error")
	}
}

// hasBranchRow reports whether the default-branch row is in the tree.
func hasBranchRow(a *App) bool {
	for _, r := range a.Rows() {
		if r.Node.Kind == NodeBranch {
			return true
		}
	}
	return false
}

func newBranchApp(t *testing.T) *App {
	t.Helper()
	a := NewApp("exampleorg/widget")
	a.Viewer = "alice"
	a.SetData(appPRs(), sampleBranch(), testNow)
	return a
}

func TestBranchRowShownByDefault(t *testing.T) {
	a := newBranchApp(t)
	if !hasBranchRow(a) {
		t.Fatal("the default branch should be shown unless switched off")
	}
	// It is the first row under the repository.
	if a.Rows()[1].Node.Kind != NodeBranch {
		t.Errorf("row 1 = %v, want the branch", a.Rows()[1].Node.Kind)
	}
}

func TestHideMainOmitsBranchRow(t *testing.T) {
	a := NewApp("exampleorg/widget")
	a.HideMain(true)
	a.SetData(appPRs(), sampleBranch(), testNow)
	if hasBranchRow(a) {
		t.Error("--no-main should omit the branch row")
	}
	// The pull requests are untouched.
	if got := prNumbers(a); !equalInts(got, []int{1, 2, 3, 4}) {
		t.Errorf("pull requests = %v", got)
	}
}

func TestToggleMainKey(t *testing.T) {
	a := newBranchApp(t)
	a.Key("m", testNow)
	if hasBranchRow(a) {
		t.Error("m should hide the branch row")
	}
	a.Key("m", testNow)
	if !hasBranchRow(a) {
		t.Error("m should bring it back")
	}
}

// The branch is not a pull request: a filter that matches no PR must still
// leave the row that says whether the repository itself is healthy.
func TestFiltersDoNotHideBranch(t *testing.T) {
	a := newBranchApp(t)
	for _, f := range []string{"f", "f", "f"} {
		a.Key(f, testNow)
		if !hasBranchRow(a) {
			t.Errorf("filter %q hid the default branch", a.filter)
		}
	}
	// Including a filter that matches nothing at all.
	a.Viewer = "nobody"
	a.filter = FilterMine
	a.rebuild(testNow)
	if len(prNumbers(a)) != 0 {
		t.Fatal("expected no matching pull requests")
	}
	if !hasBranchRow(a) {
		t.Error("an empty filter result should still show the default branch")
	}
}

func TestHideDraftsDoesNotHideBranch(t *testing.T) {
	a := newBranchApp(t)
	a.Key("d", testNow)
	if !hasBranchRow(a) {
		t.Error("hiding drafts should not touch the default branch")
	}
}

func TestBranchWorkflowFoldsByState(t *testing.T) {
	a := newBranchApp(t)
	// The branch row itself is open, its failing workflow unfolds, its passing
	// one stays folded -- the same rule pull requests follow.
	if !a.Rows()[findRow(a, "branch:main")].Expanded {
		t.Error("the branch row should be open")
	}
	if !a.Rows()[findRow(a, "branch:main/wf:Docs")].Expanded {
		t.Error("a failing workflow on main should unfold")
	}
	if a.Rows()[findRow(a, "branch:main/wf:CI")].Expanded {
		t.Error("a passing workflow on main should stay folded")
	}
}

func TestCursorSurvivesRefreshOnBranchCheck(t *testing.T) {
	a := newBranchApp(t)
	key := "branch:main/wf:Docs/check:docs"
	idx := findRow(a, key)
	if idx < 0 {
		t.Fatalf("missing row; have %v", rowLabels(a.Rows()))
	}
	a.sel = idx
	a.SetData(appPRs(), sampleBranch(), testNow.Add(time.Minute))
	if got := a.Selected(); got == nil || got.Key != key {
		t.Errorf("cursor moved to %v, want %q", got, key)
	}
}

func TestOpenURLOnBranchNodes(t *testing.T) {
	a := newBranchApp(t)
	// A check under the branch has its own run URL.
	a.sel = findRow(a, "branch:main/wf:Docs/check:docs")
	if got := a.OpenURL(); got == "" {
		t.Error("a branch check should have a URL")
	}
	// The workflow row has none, so it falls back to the commit.
	a.sel = findRow(a, "branch:main/wf:Docs")
	if got := a.OpenURL(); got != "https://example.test/commit/abc1234" {
		t.Errorf("fallback = %q, want the commit URL", got)
	}
}

// J and K step between pull requests; the branch is not one.
func TestJumpSkipsBranch(t *testing.T) {
	a := newBranchApp(t)
	a.sel = 0
	a.Key("J", testNow)
	if n := a.Selected(); n == nil || n.Kind != NodePR {
		t.Errorf("J landed on %v, want a pull request", n)
	}
}

func rowFor(a *App, key string) (Row, bool) {
	for _, r := range a.Rows() {
		if r.Node.Key == key {
			return r, true
		}
	}
	return Row{}, false
}

func TestAutoCollapseFoldsFinishedSections(t *testing.T) {
	a := newTestApp(t)
	// The failing workflow opens itself by default.
	if r, ok := rowFor(a, "pr:1/wf:CI"); !ok || !r.Expanded {
		t.Fatalf("failing workflow should start expanded: %+v", r)
	}

	a.Key("a", testNow)
	if r, ok := rowFor(a, "pr:1"); !ok || r.Expanded {
		t.Errorf("PR 1 has only finished checks and should fold: %+v", r)
	}
	if r, ok := rowFor(a, "pr:2/wf:CI"); !ok || !r.Expanded {
		t.Errorf("PR 2 is still running and should stay open: %+v", r)
	}

	a.Key("a", testNow)
	if r, _ := rowFor(a, "pr:1"); !r.Expanded {
		t.Errorf("toggling off should restore the default expansion")
	}
}

func TestAutoCollapseRespectsExplicitToggle(t *testing.T) {
	a := newTestApp(t)
	a.Key("a", testNow)
	a.overrides["pr:1"] = true
	a.rebuildPreservingCursor(testNow)
	if r, _ := rowFor(a, "pr:1"); !r.Expanded {
		t.Errorf("a section the user opened by hand must stay open")
	}
}

func TestAutoCollapseIgnoresPRWithoutChecks(t *testing.T) {
	a := NewApp("o/r")
	a.SetData([]PullRequest{{Number: 9, Title: "bare"}}, nil, testNow)
	a.Key("a", testNow)
	if r, _ := rowFor(a, "pr:9"); !r.Expanded {
		t.Errorf("a PR with no checks has nothing finished to fold")
	}
}

func TestHighlightFinishedPR(t *testing.T) {
	a := newTestApp(t)
	th := Theme{Color: true}
	find := func(num string) string {
		for i, r := range a.Rows() {
			if r.Node.Kind == NodePR && strings.HasPrefix(r.Node.Label, num+" ") {
				return a.rowLine(th, 80, i)
			}
		}
		t.Fatalf("no row for %s", num)
		return ""
	}

	if strings.Contains(find("#3"), ansiGreenBG) {
		t.Fatal("highlight must be off by default")
	}
	a.Key("H", testNow)
	if strings.Contains(find("#3"), ansiGreenBG) {
		t.Errorf("PR finished minutes ago should not be green")
	}
	// Move the clock to 10s after PR 3's last check completed, then past 30s.
	done := a.prs[2].Checks[0].CompletedAt
	a.Tick(done.Add(10 * time.Second))
	if !strings.Contains(find("#3"), ansiGreenBG) {
		t.Errorf("recently finished PR should be green")
	}
	a.Tick(done.Add(31 * time.Second))
	if strings.Contains(find("#3"), ansiGreenBG) {
		t.Errorf("highlight should expire after 30s")
	}
	a.Tick(done.Add(10 * time.Second))
	if strings.Contains(find("#1"), ansiGreenBG) {
		t.Errorf("a failed PR must not be green")
	}
	if strings.Contains(find("#2"), ansiGreenBG) {
		t.Errorf("a running PR must not be green")
	}
}
