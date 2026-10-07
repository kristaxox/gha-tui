package ghatui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ANSI attributes. Kept as raw strings rather than pulled from a styling
// library so the tool stays a single dependency-free binary.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiMagent = "\x1b[35m"
	ansiCyan   = "\x1b[36m"
	// Chrome -- tree prefixes, the detail column, the key bar -- uses the
	// terminal's own foreground dimmed rather than colour 90 ("bright black"),
	// which many dark themes render as near-black and so nearly invisible.
	ansiGrey   = "\x1b[2m"
	ansiInvert = "\x1b[7m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Theme decides whether escape codes are emitted at all. Colour off still
// produces a readable tree, which is what --once piped into a file needs.
type Theme struct {
	Color bool
	ASCII bool
}

func (t Theme) paint(attr, s string) string {
	if !t.Color || attr == "" {
		return s
	}
	return attr + s + ansiReset
}

// symbol returns the status glyph and its colour for a state.
func (t Theme) symbol(s State, spin int) (glyph, attr string) {
	if t.ASCII {
		switch s {
		case StateSuccess:
			return "+", ansiGreen
		case StateFailure:
			return "x", ansiRed
		case StateRunning:
			return "*", ansiYellow
		case StatePending:
			return ".", ansiYellow
		case StateSkipped:
			return "-", ansiGrey
		case StateCancelled:
			return "/", ansiMagent
		case StateNeutral:
			return "=", ansiGrey
		default:
			return "?", ansiGrey
		}
	}
	switch s {
	case StateSuccess:
		return "✓", ansiGreen
	case StateFailure:
		return "✗", ansiRed
	case StateRunning:
		return spinnerFrames[spin%len(spinnerFrames)], ansiYellow
	case StatePending:
		return "○", ansiYellow
	case StateSkipped:
		return "–", ansiGrey
	case StateCancelled:
		return "⊘", ansiMagent
	case StateNeutral:
		return "•", ansiGrey
	default:
		return "?", ansiGrey
	}
}

// seg is a run of text with one attribute; keeping them apart is what lets the
// line be truncated on visible width without cutting an escape sequence.
type seg struct {
	text string
	attr string
}

type lineBuf struct {
	segs  []seg
	width int
}

func (l *lineBuf) add(text, attr string) {
	if text == "" {
		return
	}
	l.segs = append(l.segs, seg{text: text, attr: attr})
	l.width += utf8.RuneCountInString(text)
}

// truncate shortens the line to n visible columns, ending in an ellipsis.
func (l *lineBuf) truncate(n int) {
	if l.width <= n || n <= 0 {
		return
	}
	keep := n - 1
	out := []seg{}
	used := 0
	for _, s := range l.segs {
		w := utf8.RuneCountInString(s.text)
		if used+w <= keep {
			out = append(out, s)
			used += w
			continue
		}
		room := keep - used
		if room > 0 {
			out = append(out, seg{text: string([]rune(s.text)[:room]), attr: s.attr})
			used += room
		}
		break
	}
	out = append(out, seg{text: "…", attr: ansiDim})
	l.segs = out
	l.width = used + 1
}

func (l *lineBuf) render(t Theme) string {
	var b strings.Builder
	for _, s := range l.segs {
		b.WriteString(t.paint(s.attr, s.text))
	}
	return b.String()
}

// Render draws the whole frame at the given size and returns one string per
// line, already padded and coloured.
func (a *App) Render(t Theme, width, height int, now time.Time) []string {
	if width < 20 {
		width = 20
	}
	if height < 6 {
		height = 6
	}
	body := height - 3 // header, status, keys
	a.high = body

	lines := make([]string, 0, height)
	lines = append(lines, a.header(t, width, now))

	if a.showHelp {
		lines = append(lines, helpLines(t, body)...)
	} else {
		lines = append(lines, a.treeLines(t, width, body)...)
	}

	lines = append(lines, a.statusLine(t, width, now), a.keyLine(t, width))
	return lines
}

func (a *App) header(t Theme, width int, now time.Time) string {
	var l lineBuf
	l.add(" gha-tui ", ansiBold)
	l.add("· ", ansiGrey)
	l.add(a.Repo, ansiCyan)

	right := ""
	switch {
	case a.loading:
		right = "refreshing…"
	case !a.lastRefresh.IsZero():
		right = "updated " + Ago(a.lastRefresh, now)
	}
	if a.filter != FilterAll {
		right = "filter: " + a.filter.String() + "  " + right
	}
	return pad(l, t, width, right, ansiGrey)
}

func (a *App) treeLines(t Theme, width, height int) []string {
	// The root row always exists, so an empty result is one with no pull
	// requests under it rather than no rows at all. The default branch row is
	// content too, so it keeps the tree on screen when no pull request is open.
	if a.noPullRequests() && a.visibleBranch() == nil {
		msg := "no pull requests match this filter"
		out := []string{"", "  " + t.paint(ansiGrey, msg)}
		for len(out) < height {
			out = append(out, "")
		}
		return out[:height]
	}

	a.scrollTo(height)
	out := make([]string, 0, height)
	end := a.top + height
	if end > len(a.rows) {
		end = len(a.rows)
	}
	for i := a.top; i < end; i++ {
		out = append(out, a.rowLine(t, width, i))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// noPullRequests reports whether the tree holds no pull requests, which is what
// a filter matching nothing looks like once the repo row is discounted.
func (a *App) noPullRequests() bool {
	for _, r := range a.rows {
		if r.Node.Kind == NodePR {
			return false
		}
	}
	return true
}

// scrollTo keeps the cursor inside the viewport with a one-row margin, so
// moving down does not park the selection on the bottom edge.
func (a *App) scrollTo(height int) {
	if height <= 0 {
		return
	}
	margin := 1
	if height < 6 {
		margin = 0
	}
	if a.sel-margin < a.top {
		a.top = a.sel - margin
	}
	if a.sel+margin >= a.top+height {
		a.top = a.sel + margin - height + 1
	}
	if a.top > len(a.rows)-height {
		a.top = len(a.rows) - height
	}
	if a.top < 0 {
		a.top = 0
	}
}

func (a *App) rowLine(t Theme, width, i int) string {
	row := a.rows[i]
	n := row.Node
	selected := i == a.sel

	var l lineBuf
	if selected {
		l.add("›", ansiBold)
	} else {
		l.add(" ", "")
	}
	l.add(row.Prefix, ansiGrey)

	if n.Kind != NodeRepo {
		sym, attr := t.symbol(n.State, a.spinner)
		l.add(sym+" ", attr)
	}

	switch n.Kind {
	case NodeRepo:
		l.add(n.Label, ansiBold)
	case NodeBranch:
		l.add(n.Label, ansiMagent)
		if n.Branch != nil {
			if sha := n.Branch.ShortSHA(); sha != "" {
				l.add(" "+sha, ansiBlue)
			}
			if n.Branch.Headline != "" {
				l.add("  "+n.Branch.Headline, ansiGrey)
			}
		}
	case NodePR:
		num, rest, _ := strings.Cut(n.Label, " ")
		l.add(num+" ", ansiBlue)
		attr := ""
		if selected {
			attr = ansiBold
		}
		l.add(rest, attr)
		if n.PR != nil && n.PR.Draft {
			l.add(" draft", ansiGrey)
		}
	case NodeWorkflow:
		l.add(n.Label, ansiBold)
	default:
		l.add(n.Label, "")
	}

	if len(n.Children) > 0 && !row.Expanded {
		l.add(fmt.Sprintf(" (%d)", len(n.Children)), ansiGrey)
	}

	line := pad(l, t, width, n.Detail, ansiGrey)
	if selected && t.Color {
		return ansiInvert + stripNested(line) + ansiReset
	}
	return line
}

// stripNested prepares a line to be wrapped in the inverse attribute: it drops
// the reset codes, so the inversion survives the coloured segments inside the
// row, and the dim ones with them.
//
// Dim has to go because the resets that would have ended it are gone: a single
// dim prefix would otherwise bleed across the whole selected row, and dim text
// on an inverted background is the least legible combination there is.
func stripNested(s string) string {
	return strings.NewReplacer(ansiReset, "", ansiDim, "").Replace(s)
}

// pad renders l left-aligned and right at the right edge, truncating the left
// side first when the two do not fit.
//
// The right column is dropped entirely when it would leave the left side no
// room, because a detail string wider than the terminal would otherwise push
// the line past the edge and wrap, corrupting the frame.
func pad(l lineBuf, t Theme, width int, right, rightAttr string) string {
	if width < 0 {
		width = 0
	}
	rightW := utf8.RuneCountInString(right)
	if right != "" {
		rightW += 2 // gap before the right column, one trailing space
	}
	// Keep at least a few columns for the left side; below that the detail is
	// not worth the space it costs.
	const minLeft = 4
	if rightW > 0 && width-rightW < minLeft {
		right, rightAttr, rightW = "", "", 0
	}
	l.truncate(width - rightW)
	gap := width - l.width - rightW
	if gap < 0 {
		gap = 0
	}
	out := l.render(t) + strings.Repeat(" ", gap)
	if right != "" {
		out += " " + t.paint(rightAttr, right) + " "
	}
	return out
}

// visible counts the columns a rendered line occupies, skipping CSI escape
// sequences. It is what the frame uses to check its own width.
func visible(s string) int {
	n := 0
	for i := 0; i < len(s); {
		// A CSI sequence is ESC [ , then parameter bytes, then a final byte in
		// @..~. The '[' itself falls in that range, so it must be skipped
		// before the scan for the terminator starts.
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

func (a *App) statusLine(t Theme, width int, _ time.Time) string {
	var l lineBuf
	switch {
	case a.lastErr != nil:
		l.add(" error: ", ansiRed)
		l.add(a.lastErr.Error(), ansiRed)
	case a.status != "":
		l.add(" "+a.status, ansiGrey)
	case a.root != nil:
		l.add(" "+a.root.Detail, ansiGrey)
	}
	return pad(l, t, width, "", "")
}

func (a *App) keyLine(t Theme, width int) string {
	var l lineBuf
	keys := " j/k move · h/l fold · ⏎ toggle · J/K next PR · e/c all · f filter · d drafts · m main · o open · r refresh · ? help · q quit"
	if t.ASCII {
		keys = strings.ReplaceAll(keys, "⏎", "enter")
		keys = strings.ReplaceAll(keys, "·", "|")
	}
	l.add(keys, ansiGrey)
	return pad(l, t, width, "", "")
}

func helpLines(t Theme, height int) []string {
	rows := [][2]string{
		{"j / k, ↓ / ↑", "move the cursor"},
		{"J / K", "jump to the next / previous pull request"},
		{"h / l, ← / →", "fold, unfold (h on a folded node goes to its parent)"},
		{"space, enter, tab", "toggle the node under the cursor"},
		{"e / c", "unfold everything / fold everything"},
		{"g / G", "first / last row"},
		{"ctrl-d / ctrl-u", "page down / up"},
		{"f", "cycle filter: all → needs attention → in flight → mine"},
		{"d", "show or hide draft pull requests"},
		{"m", "show or hide the default branch"},
		{"o", "open the selected check or pull request in a browser"},
		{"r", "refresh now"},
		{"? ", "close this help"},
		{"q, esc", "quit"},
	}
	out := []string{"", "  " + t.paint(ansiBold, "keys")}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("  %-20s %s", r[0], t.paint(ansiGrey, r[1])))
	}
	out = append(out, "", "  "+t.paint(ansiGrey, "a folded workflow that starts failing unfolds itself on the next refresh"))
	for len(out) < height {
		out = append(out, "")
	}
	return out[:height]
}

// Plain renders the tree once, without cursor, colour or chrome. This is what
// --once prints and what a script or another agent can read.
func (a *App) Plain(t Theme, width int) []string {
	out := make([]string, 0, len(a.rows))
	for _, row := range a.rows {
		n := row.Node
		var l lineBuf
		l.add(row.Prefix, ansiGrey)
		if n.Kind != NodeRepo {
			sym, attr := t.symbol(n.State, 0)
			l.add(sym+" ", attr)
		}
		l.add(n.Label, "")
		if n.Kind == NodeBranch && n.Branch != nil {
			if sha := n.Branch.ShortSHA(); sha != "" {
				l.add(" "+sha, ansiBlue)
			}
			if n.Branch.Headline != "" {
				l.add("  "+n.Branch.Headline, ansiGrey)
			}
		}
		if len(n.Children) > 0 && !row.Expanded {
			l.add(fmt.Sprintf(" (%d)", len(n.Children)), ansiGrey)
		}
		out = append(out, strings.TrimRight(pad(l, t, width, n.Detail, ansiGrey), " "))
	}
	return out
}
