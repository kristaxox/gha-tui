package ghatui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeRunner replays recorded output, so the fetch path is exercised with no
// network and no gh binary.
type fakeRunner struct {
	out  []byte
	err  error
	args []string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.args = args
	return f.out, f.err
}

func loadFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/prs.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return ts
}

func TestParsePRs(t *testing.T) {
	prs, err := parsePRs(loadFixture(t))
	if err != nil {
		t.Fatalf("parsePRs: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d pull requests, want 3", len(prs))
	}

	pr := prs[0]
	if pr.Number != 336 || pr.Author != "alice" {
		t.Errorf("first PR = #%d by %q, want #336 by alice", pr.Number, pr.Author)
	}
	if pr.Branch != "feature/boot-simulator" || pr.HeadSHA != "aaaa111" {
		t.Errorf("branch/sha = %q/%q", pr.Branch, pr.HeadSHA)
	}
	if pr.ReviewDecision != "REVIEW_REQUIRED" {
		t.Errorf("review decision = %q", pr.ReviewDecision)
	}
	if len(pr.Checks) != 6 {
		t.Fatalf("got %d checks on #336, want 6", len(pr.Checks))
	}

	// Each of the states the fixture is built to cover.
	want := map[string]struct {
		state    State
		workflow string
	}{
		"mobile-build":     {StateSuccess, "Mobile"},
		"mobile-test":      {StateFailure, "Mobile"},
		"mobile-lint":      {StateSkipped, "Mobile"},
		"mobile-archive":   {StatePending, "Mobile"},
		"build":            {StateSuccess, "CI"},
		"coverage/project": {StateSuccess, "status checks"},
	}
	for _, c := range pr.Checks {
		w, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected check %q", c.Name)
			continue
		}
		if c.State != w.state {
			t.Errorf("check %q state = %v, want %v", c.Name, c.State, w.state)
		}
		if c.Workflow != w.workflow {
			t.Errorf("check %q workflow = %q, want %q", c.Name, c.Workflow, w.workflow)
		}
	}

	// A queued run has no start time, so it has no duration to show.
	for _, c := range pr.Checks {
		if c.Name == "mobile-archive" {
			if !c.StartedAt.IsZero() {
				t.Errorf("queued check has a start time: %v", c.StartedAt)
			}
			if d := c.Duration(mustTime(t, "2024-05-01T12:00:00Z")); d != 0 {
				t.Errorf("queued check duration = %v, want 0", d)
			}
		}
	}

	// An in-progress run has no completion, so its duration runs to now.
	running := prs[1].Checks[0]
	if running.State != StateRunning {
		t.Errorf("record-versions state = %v, want running", running.State)
	}
	if !running.CompletedAt.IsZero() {
		t.Errorf("running check has a completion time: %v", running.CompletedAt)
	}
	if got := running.Duration(mustTime(t, "2024-05-01T11:50:00Z")); got != 4*time.Minute {
		t.Errorf("running duration = %v, want 4m", got)
	}

	// A PR whose rollup is null must still parse, with no checks.
	if len(prs[2].Checks) != 0 {
		t.Errorf("draft PR has %d checks, want 0", len(prs[2].Checks))
	}
	if !prs[2].Draft {
		t.Error("PR #338 should be a draft")
	}
}

func TestParsePRsInvalidJSON(t *testing.T) {
	if _, err := parsePRs([]byte("not json")); err == nil {
		t.Fatal("parsePRs accepted invalid JSON")
	}
}

func TestFetchBuildsQuery(t *testing.T) {
	r := &fakeRunner{out: loadFixture(t)}
	prs, branch, err := Fetch(context.Background(), r, "exampleorg/widget", 5)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d pull requests, want 3", len(prs))
	}
	if branch == nil || branch.Name != "main" {
		t.Fatalf("Fetch returned branch %v, want main", branch)
	}
	joined := r.args
	if len(joined) == 0 || joined[0] != "api" || joined[1] != "graphql" {
		t.Fatalf("Fetch invoked gh with %v", joined)
	}
	var sawOwner, sawName, sawLimit bool
	for _, a := range joined {
		switch a {
		case "owner=exampleorg":
			sawOwner = true
		case "name=widget":
			sawName = true
		case "limit=5":
			sawLimit = true
		}
	}
	if !sawOwner || !sawName || !sawLimit {
		t.Errorf("missing variables in %v", joined)
	}
}

func TestFetchPropagatesError(t *testing.T) {
	want := errors.New("gh exploded")
	r := &fakeRunner{err: want}
	if _, _, err := Fetch(context.Background(), r, "a/b", 10); !errors.Is(err, want) {
		t.Fatalf("Fetch error = %v, want %v", err, want)
	}
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := SplitRepo(" exampleorg/widget ")
	if err != nil {
		t.Fatalf("SplitRepo: %v", err)
	}
	if owner != "exampleorg" || name != "widget" {
		t.Errorf("got %q/%q", owner, name)
	}
	for _, bad := range []string{"", "noslash", "/name", "owner/"} {
		if _, _, err := SplitRepo(bad); err == nil {
			t.Errorf("SplitRepo(%q) accepted a malformed repository", bad)
		}
	}
}

func TestCurrentRepo(t *testing.T) {
	r := &fakeRunner{out: []byte("exampleorg/widget\n")}
	repo, err := CurrentRepo(context.Background(), r)
	if err != nil {
		t.Fatalf("CurrentRepo: %v", err)
	}
	if repo != "exampleorg/widget" {
		t.Errorf("got %q", repo)
	}

	empty := &fakeRunner{out: []byte("  \n")}
	if _, err := CurrentRepo(context.Background(), empty); err == nil {
		t.Error("CurrentRepo accepted empty output")
	}
}

func TestParseBranch(t *testing.T) {
	branch, err := parseBranch(loadFixture(t))
	if err != nil {
		t.Fatalf("parseBranch: %v", err)
	}
	if branch == nil {
		t.Fatal("parseBranch returned no branch")
	}
	if branch.Name != "main" {
		t.Errorf("name = %q, want main", branch.Name)
	}
	if branch.ShortSHA() != "d4d4d4d" {
		t.Errorf("short sha = %q, want d4d4d4d", branch.ShortSHA())
	}
	if !strings.Contains(branch.Headline, "Merge pull request") {
		t.Errorf("headline = %q", branch.Headline)
	}
	if branch.URL == "" {
		t.Error("branch has no commit URL to open")
	}
	if len(branch.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(branch.Checks))
	}
	// main is failing in the fixture, which is the interesting case.
	if branch.State() != StateFailure {
		t.Errorf("state = %v, want failure", branch.State())
	}
	passed, total := branch.Counts()
	if passed != 1 || total != 2 {
		t.Errorf("counts = %d/%d, want 1/2", passed, total)
	}
}

// A repository with no commits reports a null defaultBranchRef; that must be a
// nil branch rather than an error or an empty row.
func TestParseBranchAbsent(t *testing.T) {
	branch, err := parseBranch([]byte(`{"data":{"repository":{"defaultBranchRef":null,"pullRequests":{"nodes":[]}}}}`))
	if err != nil {
		t.Fatalf("parseBranch: %v", err)
	}
	if branch != nil {
		t.Errorf("got %v, want nil", branch)
	}
}

func TestParseBranchInvalidJSON(t *testing.T) {
	if _, err := parseBranch([]byte("{{{")); err == nil {
		t.Error("parseBranch accepted invalid JSON")
	}
}

func TestShortSHAHandlesShortInput(t *testing.T) {
	if got := (Branch{SHA: "abc"}).ShortSHA(); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
	if got := (Branch{}).ShortSHA(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
