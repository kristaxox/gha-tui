package ghatui

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

// sampleePRs builds a small tree by hand so the expected shape is obvious in
// the test rather than buried in a fixture.
func samplePRs() []PullRequest {
	start := testNow.Add(-10 * time.Minute)
	return []PullRequest{
		{
			Number: 1, Title: "first", Author: "alice", URL: "https://example.test/pr/1",
			UpdatedAt: testNow.Add(-2 * time.Minute), Mergeable: "MERGEABLE",
			Checks: []Check{
				{Name: "build", Workflow: "CI", State: StateSuccess, StartedAt: start, CompletedAt: start.Add(time.Minute)},
				{Name: "test", Workflow: "CI", State: StateFailure, StartedAt: start, CompletedAt: start.Add(2 * time.Minute)},
			},
		},
		{
			Number: 2, Title: "second", Author: "bob", URL: "https://example.test/pr/2",
			UpdatedAt: testNow.Add(-30 * time.Second),
			Checks: []Check{
				{Name: "lint", Workflow: "CI", State: StateRunning, StartedAt: start},
			},
		},
	}
}

func alwaysExpanded(*Node) bool { return true }

func TestBuildTreeShape(t *testing.T) {
	root := BuildTree("exampleorg/widget", nil, samplePRs(), testNow)

	if root.Kind != NodeRepo || root.Label != "exampleorg/widget" {
		t.Fatalf("root = %v %q", root.Kind, root.Label)
	}
	// The repo shows the worst state below it.
	if root.State != StateFailure {
		t.Errorf("root state = %v, want failure", root.State)
	}
	if want := "2 open · 1 failing · 1 in flight"; root.Detail != want {
		t.Errorf("root detail = %q, want %q", root.Detail, want)
	}
	if len(root.Children) != 2 {
		t.Fatalf("got %d PR nodes, want 2", len(root.Children))
	}

	pr := root.Children[0]
	if pr.Key != "pr:1" || pr.Label != "#1 first" {
		t.Errorf("PR node = %q %q", pr.Key, pr.Label)
	}
	if !strings.Contains(pr.Detail, "alice") || !strings.Contains(pr.Detail, "1/2") {
		t.Errorf("PR detail = %q, want author and 1/2", pr.Detail)
	}
	wf := pr.Children[0]
	if wf.Key != "pr:1/wf:CI" || wf.State != StateFailure {
		t.Errorf("workflow node = %q %v", wf.Key, wf.State)
	}
	if wf.Children[0].Key != "pr:1/wf:CI/check:build" {
		t.Errorf("check key = %q", wf.Children[0].Key)
	}
}

func TestBuildTreeNoChecks(t *testing.T) {
	prs := []PullRequest{{Number: 9, Title: "bare", Author: "alice"}}
	root := BuildTree("exampleorg/widget", nil, prs, testNow)
	pr := root.Children[0]
	if len(pr.Children) != 1 {
		t.Fatalf("got %d children, want the placeholder", len(pr.Children))
	}
	if pr.Children[0].Label != "no checks reported" {
		t.Errorf("placeholder = %q", pr.Children[0].Label)
	}
	if !strings.Contains(pr.Detail, "no checks") {
		t.Errorf("detail = %q", pr.Detail)
	}
}

func TestPRDetailConflictsAndReview(t *testing.T) {
	prs := []PullRequest{{
		Number: 3, Title: "t", Author: "alice",
		ReviewDecision: "CHANGES_REQUESTED", Mergeable: "CONFLICTING",
		UpdatedAt: testNow.Add(-time.Hour),
		Checks:    []Check{{Name: "a", Workflow: "CI", State: StateSuccess}},
	}}
	detail := BuildTree("r", nil, prs, testNow).Children[0].Detail
	for _, want := range []string{"alice", "1/1", "changes requested", "conflicts", "1h ago"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q missing %q", detail, want)
		}
	}
}

func TestFlattenPrefixes(t *testing.T) {
	root := BuildTree("exampleorg/widget", nil, samplePRs(), testNow)
	rows := Flatten(root, alwaysExpanded)

	// repo, pr1, wf, build, test, pr2, wf, lint
	if len(rows) != 8 {
		t.Fatalf("got %d rows, want 8", len(rows))
	}
	want := []struct {
		prefix string
		label  string
	}{
		{"", "exampleorg/widget"},
		{"├─ ", "#1 first"},
		{"│  └─ ", "CI"},
		{"│     ├─ ", "build"},
		{"│     └─ ", "test"},
		{"└─ ", "#2 second"},
		{"   └─ ", "CI"},
		{"      └─ ", "lint"},
	}
	for i, w := range want {
		if rows[i].Prefix != w.prefix {
			t.Errorf("row %d prefix = %q, want %q", i, rows[i].Prefix, w.prefix)
		}
		if rows[i].Node.Label != w.label {
			t.Errorf("row %d label = %q, want %q", i, rows[i].Node.Label, w.label)
		}
	}
}

func TestFlattenStopsAtFoldedNodes(t *testing.T) {
	root := BuildTree("exampleorg/widget", nil, samplePRs(), testNow)
	// Fold everything below the pull requests.
	rows := Flatten(root, func(n *Node) bool { return n.Kind == NodeRepo || n.Kind == NodePR })
	// repo + 2 PRs + the one workflow under each, but none of their checks.
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %v", len(rows), rowLabels(rows))
	}
	for _, r := range rows {
		if r.Node.Kind == NodeCheck {
			t.Errorf("a folded workflow leaked a check row: %q", r.Node.Label)
		}
	}
}

func rowLabels(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Node.Label
	}
	return out
}

func TestDur(t *testing.T) {
	tests := map[time.Duration]string{
		0:                              "0s",
		45 * time.Second:               "45s",
		4*time.Minute + 12*time.Second: "4m12s",
		time.Minute + 5*time.Second:    "1m05s",
		time.Hour + 2*time.Minute:      "1h02m",
		-time.Second:                   "0s",
	}
	for in, want := range tests {
		if got := Dur(in); got != want {
			t.Errorf("Dur(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAgo(t *testing.T) {
	tests := []struct {
		offset time.Duration
		want   string
	}{
		{0, "just now"},
		{-30 * time.Second, "just now"},
		{-5 * time.Minute, "5m ago"},
		{-3 * time.Hour, "3h ago"},
		{-50 * time.Hour, "2d ago"},
		{time.Hour, "just now"}, // a future timestamp must not go negative
	}
	for _, tt := range tests {
		if got := Ago(testNow.Add(tt.offset), testNow); got != tt.want {
			t.Errorf("Ago(%v) = %q, want %q", tt.offset, got, tt.want)
		}
	}
}

func TestGroupDurationSpansWholeWorkflow(t *testing.T) {
	start := testNow.Add(-10 * time.Minute)
	g := workflowGroup{Checks: []Check{
		{StartedAt: start, CompletedAt: start.Add(time.Minute)},
		{StartedAt: start.Add(30 * time.Second), CompletedAt: start.Add(3 * time.Minute)},
		{}, // queued: contributes nothing
	}}
	if got := groupDuration(g, testNow); got != "3m00s" {
		t.Errorf("groupDuration = %q, want 3m00s", got)
	}
	if got := groupDuration(workflowGroup{Checks: []Check{{}}}, testNow); got != "" {
		t.Errorf("all-queued workflow duration = %q, want empty", got)
	}
}

func TestCheckDetail(t *testing.T) {
	start := testNow.Add(-90 * time.Second)
	if got := checkDetail(Check{State: StatePending}, testNow); got != "queued" {
		t.Errorf("queued detail = %q", got)
	}
	if got := checkDetail(Check{State: StateSuccess, StartedAt: start, CompletedAt: testNow}, testNow); got != "1m30s" {
		t.Errorf("finished detail = %q, want 1m30s", got)
	}
	if got := checkDetail(Check{State: StateSuccess}, testNow); got != "" {
		t.Errorf("no-duration detail = %q, want empty", got)
	}
}

func sampleBranch() *Branch {
	start := testNow.Add(-20 * time.Minute)
	return &Branch{
		Name:      "main",
		SHA:       "abc1234def5678",
		Headline:  "Merge pull request #330",
		URL:       "https://example.test/commit/abc1234",
		Committed: testNow.Add(-90 * time.Minute),
		Checks: []Check{
			{Name: "build", Workflow: "CI", State: StateSuccess, StartedAt: start, CompletedAt: start.Add(time.Minute)},
			{Name: "docs", Workflow: "Docs", State: StateFailure, StartedAt: start, CompletedAt: start.Add(2 * time.Minute)},
		},
	}
}

func TestBuildTreePlacesBranchFirst(t *testing.T) {
	root := BuildTree("exampleorg/widget", sampleBranch(), samplePRs(), testNow)
	if len(root.Children) != 3 {
		t.Fatalf("got %d children, want the branch plus 2 PRs", len(root.Children))
	}
	b := root.Children[0]
	if b.Kind != NodeBranch {
		t.Fatalf("first child is %v, want the branch", b.Kind)
	}
	if b.Key != "branch:main" || b.Label != "main" {
		t.Errorf("branch node = %q %q", b.Key, b.Label)
	}
	if b.State != StateFailure {
		t.Errorf("branch state = %v, want failure", b.State)
	}
	for _, want := range []string{"1/2", "1h ago"} {
		if !strings.Contains(b.Detail, want) {
			t.Errorf("branch detail %q missing %q", b.Detail, want)
		}
	}
	// The hash identifies the row, so it sits by the name rather than in the
	// detail column, where a long headline would push it off the edge.
	if strings.Contains(b.Detail, "abc1234") {
		t.Errorf("branch detail %q should not carry the hash", b.Detail)
	}
	// It has the same workflow -> check shape as a pull request.
	if len(b.Children) != 2 {
		t.Fatalf("got %d workflows under the branch", len(b.Children))
	}
	if b.Children[0].Key != "branch:main/wf:Docs" {
		t.Errorf("worst workflow first: got %q", b.Children[0].Key)
	}
	if b.Children[0].Children[0].Key != "branch:main/wf:Docs/check:docs" {
		t.Errorf("check key = %q", b.Children[0].Children[0].Key)
	}
	// The pull requests still follow, unchanged.
	if root.Children[1].Key != "pr:1" {
		t.Errorf("second child = %q, want pr:1", root.Children[1].Key)
	}
}

// The repo summary counts pull requests; a failing main must not inflate it.
func TestBranchDoesNotChangeRepoCounts(t *testing.T) {
	withBranch := BuildTree("r", sampleBranch(), samplePRs(), testNow)
	without := BuildTree("r", nil, samplePRs(), testNow)
	if withBranch.Detail != without.Detail {
		t.Errorf("repo detail changed with a branch: %q vs %q", withBranch.Detail, without.Detail)
	}
}

func TestBuildTreeNilBranchOmitsRow(t *testing.T) {
	root := BuildTree("r", nil, samplePRs(), testNow)
	for _, c := range root.Children {
		if c.Kind == NodeBranch {
			t.Fatal("a nil branch should add no row")
		}
	}
}

func TestBranchWithNoChecks(t *testing.T) {
	b := &Branch{Name: "main", SHA: "abc1234"}
	root := BuildTree("r", b, nil, testNow)
	node := root.Children[0]
	if len(node.Children) != 1 || node.Children[0].Label != "no checks reported" {
		t.Errorf("got %v", rowLabels(Flatten(root, alwaysExpanded)))
	}
	if !strings.Contains(node.Detail, "no checks") {
		t.Errorf("detail = %q", node.Detail)
	}
}
