package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kristaxox/gha-tui/internal/ghatui"
)

// stubRunner answers gh invocations from a table, so the resolution paths can
// be tested with no gh binary and no network.
type stubRunner struct {
	repoOut string
	repoErr error
	userOut string
	userErr error
}

func (s stubRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "repo view"):
		return []byte(s.repoOut), s.repoErr
	case strings.HasPrefix(joined, "api user"):
		return []byte(s.userOut), s.userErr
	}
	return nil, errors.New("unexpected gh call: " + joined)
}

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", opts.interval)
	}
	if opts.limit != 30 {
		t.Errorf("limit = %d, want 30", opts.limit)
	}
	if opts.once || opts.noColor || opts.ascii || opts.repo != "" {
		t.Errorf("unexpected defaults: %+v", opts)
	}
}

func TestParseFlagsValues(t *testing.T) {
	opts, err := parseFlags([]string{
		"--repo", "exampleorg/widget",
		"--interval", "5s",
		"--limit", "10",
		"--once", "--no-color", "--ascii",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.repo != "exampleorg/widget" || opts.interval != 5*time.Second || opts.limit != 10 {
		t.Errorf("got %+v", opts)
	}
	if !opts.once || !opts.noColor || !opts.ascii {
		t.Errorf("boolean flags not set: %+v", opts)
	}
}

// --interval 0 is the documented way to disable auto-refresh, so it must parse.
func TestParseFlagsZeroIntervalIsValid(t *testing.T) {
	opts, err := parseFlags([]string{"--interval", "0"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.interval != 0 {
		t.Errorf("interval = %v, want 0", opts.interval)
	}
}

func TestParseFlagsRejectsBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"--limit", "0"},
		{"--limit", "-3"},
		{"--interval", "-5s"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) accepted an invalid value", args)
		}
	}
}

func TestParseFlagsUnknownFlag(t *testing.T) {
	// flag prints to the FlagSet's output; silence it for the test run.
	if _, err := parseFlagsQuiet([]string{"--nope"}); err == nil {
		t.Error("parseFlags accepted an unknown flag")
	}
}

// parseFlagsQuiet is parseFlags with the usage output discarded.
func parseFlagsQuiet(args []string) (options, error) {
	old := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return parseFlags(args)
	}
	os.Stderr = devNull
	defer func() {
		os.Stderr = old
		_ = devNull.Close()
	}()
	return parseFlags(args)
}

func TestResolveRepoPrefersFlag(t *testing.T) {
	r := stubRunner{repoOut: "other/repo\n"}
	if got, err := resolveRepo(context.Background(), r, " exampleorg/widget "); err != nil || got != "exampleorg/widget" {
		t.Errorf("got %q, want the flag value trimmed", got)
	}
}

func TestResolveRepoUsesCheckout(t *testing.T) {
	r := stubRunner{repoOut: "exampleorg/widget\n"}
	if got, err := resolveRepo(context.Background(), r, ""); err != nil || got != "exampleorg/widget" {
		t.Errorf("got %q, want the checkout's repository", got)
	}
}

// Outside a GitHub checkout with no --repo there is nothing to watch.
func TestResolveRepoErrorsOutsideCheckout(t *testing.T) {
	r := stubRunner{repoErr: errors.New("not a git repository")}
	if got, err := resolveRepo(context.Background(), r, ""); err == nil {
		t.Errorf("got %q, want an error", got)
	}
}

func TestViewerLogin(t *testing.T) {
	r := stubRunner{userOut: "alice\n"}
	if got := viewerLogin(context.Background(), r); got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
	// An unauthenticated gh just means the "mine" filter matches nothing.
	failing := stubRunner{userErr: errors.New("not logged in")}
	if got := viewerLogin(context.Background(), failing); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseSize(t *testing.T) {
	width, height, err := parseSize("40 120\n")
	if err != nil {
		t.Fatalf("parseSize: %v", err)
	}
	if width != 120 || height != 40 {
		t.Errorf("got %dx%d, want 120x40", width, height)
	}
	for _, bad := range []string{"", "40", "40 120 3", "rows cols", "40 cols"} {
		if _, _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted malformed output", bad)
		}
	}
}

// --once must render without a terminal, which is what makes it usable from a
// script or another agent.
func TestOnceWritesTreeWithoutTTY(t *testing.T) {
	fixture, err := os.ReadFile("internal/ghatui/testdata/prs.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	r := fixtureRunner{out: fixture}

	app := ghatui.NewApp("exampleorg/widget")
	out := captureStdout(t, func() {
		if err := once(context.Background(), r, app, ghatui.Theme{}, options{limit: 30}); err != nil {
			t.Fatalf("once: %v", err)
		}
	})

	if !strings.Contains(out, "exampleorg/widget") {
		t.Errorf("output missing the repo: %q", out)
	}
	if !strings.Contains(out, "#336") {
		t.Errorf("output missing a pull request: %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("--once with colour off emitted escapes: %q", out)
	}
}

func TestOnceReportsFetchFailure(t *testing.T) {
	r := fixtureRunner{err: errors.New("gh is unhappy")}
	app := ghatui.NewApp("exampleorg/widget")
	err := once(context.Background(), r, app, ghatui.Theme{}, options{limit: 30})
	if err == nil {
		t.Fatal("once should surface a fetch failure")
	}
	if !strings.Contains(err.Error(), "gh is unhappy") {
		t.Errorf("error = %v", err)
	}
}

type fixtureRunner struct {
	out []byte
	err error
}

func (f fixtureRunner) Run(_ context.Context, _ ...string) ([]byte, error) {
	return f.out, f.err
}

// captureStdout swaps os.Stdout for a pipe while fn runs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func TestParseFlagsNoMain(t *testing.T) {
	opts, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.noMain {
		t.Error("the default branch should be included by default")
	}
	opts, err = parseFlags([]string{"--no-main"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.noMain {
		t.Error("--no-main did not set the flag")
	}
}

// --once shows the default branch too, and --no-main omits it.
func TestOnceIncludesDefaultBranch(t *testing.T) {
	fixture, err := os.ReadFile("internal/ghatui/testdata/prs.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	r := fixtureRunner{out: fixture}

	app := ghatui.NewApp("exampleorg/widget")
	out := captureStdout(t, func() {
		if err := once(context.Background(), r, app, ghatui.Theme{}, options{limit: 30}); err != nil {
			t.Fatalf("once: %v", err)
		}
	})
	if !strings.Contains(out, "main") {
		t.Errorf("--once output missing the default branch: %q", out)
	}

	hidden := ghatui.NewApp("exampleorg/widget")
	hidden.HideMain(true)
	out = captureStdout(t, func() {
		if err := once(context.Background(), r, hidden, ghatui.Theme{}, options{limit: 30}); err != nil {
			t.Fatalf("once: %v", err)
		}
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Merge pull request #330") {
			t.Errorf("--no-main still printed the branch row: %q", line)
		}
	}
}
