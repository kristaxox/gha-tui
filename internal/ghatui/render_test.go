package ghatui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// visibleWidth counts the columns a rendered line actually occupies, ignoring
// the escape sequences inside it.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(stripANSI(s))
}

// stripANSI removes CSI sequences so a test can assert on what the user sees.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func TestLineBufTruncateOnVisibleWidth(t *testing.T) {
	var l lineBuf
	l.add("hello ", ansiGreen)
	l.add("world", ansiRed)
	l.truncate(8)

	out := l.render(Theme{Color: true})
	// The visible text is cut to 8 columns, ending in the ellipsis...
	if got := stripANSI(out); got != "hello w…" {
		t.Errorf("truncated text = %q, want %q", got, "hello w…")
	}
	// ...and no escape sequence was cut in half: every escape still terminates.
	if strings.Count(out, "\x1b[") != strings.Count(out, "m") {
		t.Errorf("truncation broke an escape sequence: %q", out)
	}
}

func TestLineBufTruncateNoOpWhenItFits(t *testing.T) {
	var l lineBuf
	l.add("short", "")
	l.truncate(80)
	if got := l.render(Theme{}); got != "short" {
		t.Errorf("got %q, want %q", got, "short")
	}
}

func TestLineBufTruncateMultibyte(t *testing.T) {
	var l lineBuf
	l.add("✓✓✓✓✓✓", ansiGreen) // 6 runes, 18 bytes
	l.truncate(4)
	got := stripANSI(l.render(Theme{Color: true}))
	if utf8.RuneCountInString(got) != 4 {
		t.Errorf("got %q (%d runes), want 4 runes", got, utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("got %q, want an ellipsis", got)
	}
}

func TestRenderFitsWidthAndHeight(t *testing.T) {
	a := newTestApp(t)
	for _, width := range []int{24, 40, 80, 200} {
		for _, height := range []int{8, 24, 60} {
			lines := a.Render(Theme{Color: true}, width, height, testNow)
			if len(lines) != height {
				t.Fatalf("%dx%d: got %d lines, want %d", width, height, len(lines), height)
			}
			for i, line := range lines {
				if w := visibleWidth(line); w > width {
					t.Errorf("%dx%d line %d is %d columns wide: %q", width, height, i, w, stripANSI(line))
				}
			}
		}
	}
}

func TestRenderNarrowWidthDoesNotPanic(t *testing.T) {
	a := newTestApp(t)
	// Below the clamped minimum; Render must still produce a usable frame.
	lines := a.Render(Theme{Color: true}, 1, 1, testNow)
	if len(lines) != 6 {
		t.Errorf("got %d lines, want the 6-line minimum", len(lines))
	}
}

func TestRenderHeaderShowsRepoAndFilter(t *testing.T) {
	a := newTestApp(t)
	header := stripANSI(a.Render(Theme{}, 100, 20, testNow)[0])
	if !strings.Contains(header, "exampleorg/widget") {
		t.Errorf("header = %q, want the repo", header)
	}
	a.Key("f", testNow)
	header = stripANSI(a.Render(Theme{}, 100, 20, testNow)[0])
	if !strings.Contains(header, "needs attention") {
		t.Errorf("header = %q, want the filter name", header)
	}
}

func TestRenderEmptyTree(t *testing.T) {
	a := NewApp("exampleorg/widget")
	a.SetData(nil, nil, testNow)
	lines := a.Render(Theme{}, 60, 12, testNow)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "no pull requests match this filter") {
		t.Errorf("empty tree rendered as %q", joined)
	}
}

func TestSelectedRowIsMarked(t *testing.T) {
	a := newTestApp(t)
	a.sel = 1
	lines := a.Render(Theme{}, 80, 20, testNow)
	// Without colour the cursor is the marker character.
	var marked int
	for _, line := range lines {
		if strings.HasPrefix(stripANSI(line), "›") {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("got %d marked rows, want exactly 1", marked)
	}
}

func TestScrollKeepsCursorVisibleWithMargin(t *testing.T) {
	a := newTestApp(t)
	a.Key("e", testNow) // unfold everything so there is more than one screen
	height := 6
	a.Key("G", testNow)
	a.Render(Theme{}, 80, height+3, testNow)
	if a.sel < a.top || a.sel >= a.top+height {
		t.Errorf("cursor %d outside viewport [%d,%d)", a.sel, a.top, a.top+height)
	}
	a.Key("g", testNow)
	a.Render(Theme{}, 80, height+3, testNow)
	if a.top != 0 {
		t.Errorf("top = %d after g, want 0", a.top)
	}
}

func TestThemeASCIIFallback(t *testing.T) {
	ascii := Theme{ASCII: true}
	unicode := Theme{}
	for _, s := range []State{StateSuccess, StateFailure, StateRunning, StatePending, StateSkipped, StateCancelled, StateNeutral, StateUnknown} {
		sym, _ := ascii.symbol(s, 0)
		if sym == "" {
			t.Errorf("no ASCII glyph for %v", s)
		}
		for _, r := range sym {
			if r > 127 {
				t.Errorf("ASCII glyph for %v is not ASCII: %q", s, sym)
			}
		}
		if u, _ := unicode.symbol(s, 0); u == "" {
			t.Errorf("no glyph for %v", s)
		}
	}
}

func TestThemeSpinnerAdvances(t *testing.T) {
	th := Theme{}
	first, _ := th.symbol(StateRunning, 0)
	second, _ := th.symbol(StateRunning, 1)
	if first == second {
		t.Error("the spinner should advance between frames")
	}
	// It must wrap rather than index out of range.
	if _, attr := th.symbol(StateRunning, len(spinnerFrames)*3+1); attr == "" {
		t.Error("spinner index did not wrap")
	}
}

func TestThemeColorOff(t *testing.T) {
	plain := Theme{Color: false}
	if got := plain.paint(ansiRed, "text"); got != "text" {
		t.Errorf("colour off still painted: %q", got)
	}
	colored := Theme{Color: true}
	if got := colored.paint(ansiRed, "text"); !strings.Contains(got, ansiRed) {
		t.Errorf("colour on did not paint: %q", got)
	}
}

func TestPlainOutputHasNoEscapesAndNoCursor(t *testing.T) {
	a := newTestApp(t)
	lines := a.Plain(Theme{Color: false}, 100)
	if len(lines) == 0 {
		t.Fatal("Plain produced nothing")
	}
	for _, line := range lines {
		if strings.Contains(line, "\x1b") {
			t.Errorf("Plain emitted an escape sequence: %q", line)
		}
		if strings.HasPrefix(line, "›") {
			t.Errorf("Plain drew a cursor: %q", line)
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("Plain left trailing whitespace: %q", line)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "exampleorg/widget") {
		t.Errorf("Plain output missing the repo: %q", joined)
	}
	// It shows the same tree the interactive view would, failing check included.
	if !strings.Contains(joined, "test") {
		t.Errorf("Plain output missing the failing check: %q", joined)
	}
}

func TestHelpOverlayFillsHeight(t *testing.T) {
	for _, height := range []int{8, 20, 40} {
		lines := helpLines(Theme{}, height)
		if len(lines) != height {
			t.Errorf("helpLines(%d) = %d lines", height, len(lines))
		}
	}
}

// A detail string wider than the terminal must not push the line past the
// edge: the frame wraps and corrupts if it does.
func TestPadDropsRightColumnWhenItCannotFit(t *testing.T) {
	var l lineBuf
	l.add("label", "")
	out := pad(l, Theme{}, 24, "4 open · 1 failing · 1 in flight", ansiGrey)
	if got := visibleWidth(out); got > 24 {
		t.Errorf("line is %d columns wide, want <= 24: %q", got, stripANSI(out))
	}
	if strings.Contains(stripANSI(out), "in flight") {
		t.Errorf("the oversized right column should have been dropped: %q", stripANSI(out))
	}
}

func TestPadKeepsRightColumnWhenItFits(t *testing.T) {
	var l lineBuf
	l.add("label", "")
	out := pad(l, Theme{}, 40, "1/2 · just now", ansiGrey)
	if !strings.Contains(stripANSI(out), "just now") {
		t.Errorf("right column dropped when it fits: %q", stripANSI(out))
	}
	if got := visibleWidth(out); got != 40 {
		t.Errorf("padded width = %d, want 40", got)
	}
}

// visible() is what non-test code uses to measure a rendered line.
func TestVisibleIgnoresEscapes(t *testing.T) {
	if got := visible(ansiRed + "abc" + ansiReset); got != 3 {
		t.Errorf("visible = %d, want 3", got)
	}
	if got := visible("plain"); got != 5 {
		t.Errorf("visible = %d, want 5", got)
	}
}

// Colour 90 ("bright black") is near-invisible in many dark themes, so the
// chrome dims the terminal's own foreground instead.
func TestChromeAvoidsBrightBlack(t *testing.T) {
	a := newTestApp(t)
	frame := strings.Join(a.Render(Theme{Color: true}, 100, 20, testNow), "\n")
	if strings.Contains(frame, "\x1b[90m") {
		t.Error("the frame uses colour 90, which dark themes render as near-black")
	}
	if !strings.Contains(frame, ansiDim) {
		t.Error("the chrome should be dimmed so it recedes without vanishing")
	}
}

// Dim text on an inverted background is the least legible combination there
// is, and the stripped resets would let one dim prefix bleed across the row.
func TestSelectedRowIsNotDimmed(t *testing.T) {
	a := newTestApp(t)
	a.sel = 1
	line := a.rowLine(Theme{Color: true}, 60, 1)
	if !strings.HasPrefix(line, ansiInvert) {
		t.Fatalf("selected row is not inverted: %q", line)
	}
	if strings.Contains(line, ansiDim) {
		t.Errorf("selected row carries a dim attribute: %q", line)
	}
	// The inversion must still be closed.
	if !strings.HasSuffix(line, ansiReset) {
		t.Errorf("selected row does not reset: %q", line)
	}
}

// With no open pull requests the default branch row is still the tree.
func TestRenderBranchWithoutPullRequests(t *testing.T) {
	a := NewApp("exampleorg/widget")
	a.SetData(nil, &Branch{Name: "main", SHA: "2793375ef6f8", Headline: "Merge"}, testNow)
	joined := stripANSI(strings.Join(a.Render(Theme{}, 80, 12, testNow), "\n"))
	if !strings.Contains(joined, "main") || strings.Contains(joined, "no pull requests match") {
		t.Errorf("branch row missing from an empty-PR tree:\n%s", joined)
	}
}
