package ghatui

import (
	"fmt"
	"strings"
	"time"
)

// Filter narrows which pull requests the tree shows.
type Filter int

const (
	// FilterAll shows every open pull request.
	FilterAll Filter = iota
	// FilterAttention shows the ones that are failing or have conflicts --
	// the "what do I have to fix" view.
	FilterAttention
	// FilterRunning shows the ones with work still in flight.
	FilterRunning
	// FilterMine shows the ones authored by the authenticated user.
	FilterMine
)

func (f Filter) String() string {
	switch f {
	case FilterAttention:
		return "needs attention"
	case FilterRunning:
		return "in flight"
	case FilterMine:
		return "mine"
	default:
		return "all"
	}
}

func (f Filter) next() Filter {
	if f == FilterMine {
		return FilterAll
	}
	return f + 1
}

// Action is what a keypress asks the caller to do. Everything the App can do
// on its own -- moving, expanding, filtering -- it has already done by the time
// Key returns; these are the effects that need the outside world.
type Action int

const (
	// ActionNone means the keypress was handled internally.
	ActionNone Action = iota
	// ActionQuit means leave.
	ActionQuit
	// ActionRefresh means fetch again now.
	ActionRefresh
	// ActionOpen means open the selected node's URL in a browser.
	ActionOpen
)

// App is the whole interactive state: the data, what is expanded, where the
// cursor is. It performs no IO, so the key handling and the layout are testable
// without a terminal.
type App struct {
	Repo   string
	Viewer string

	prs       []PullRequest
	branch    *Branch
	hideMain  bool
	root      *Node
	rows      []Row
	overrides map[string]bool

	sel  int
	top  int
	high int // rows of tree visible in the last Render, for paging

	filter     Filter
	hideDrafts bool
	showHelp   bool

	autoCollapse  bool // fold sections whose checks have all finished
	highlightDone bool // paint a pull request green once all its checks finished

	now         time.Time // clock of the last rebuild, for time-limited highlights
	lastRefresh time.Time
	lastErr     error
	loading     bool
	spinner     int
	status      string
}

// NewApp returns an empty App watching repo.
func NewApp(repo string) *App {
	return &App{Repo: repo, overrides: map[string]bool{}}
}

// HideMain suppresses the default-branch row, for --no-main.
func (a *App) HideMain(v bool) { a.hideMain = v }

// SetData replaces the pull requests and rebuilds the tree, keeping the cursor
// on the same node where it still exists. A refresh landing under the cursor
// must not move it -- an auto-refreshing view that jumps is unusable.
func (a *App) SetData(prs []PullRequest, branch *Branch, now time.Time) {
	selectedKey := ""
	if n := a.Selected(); n != nil {
		selectedKey = n.Key
	}
	a.prs = prs
	a.branch = branch
	a.lastRefresh = now
	a.lastErr = nil
	a.loading = false
	a.rebuild(now)
	a.restoreSelection(selectedKey)
}

// SetError records a failed refresh. The last good tree stays on screen, which
// is what you want when the network blips during a long watch.
func (a *App) SetError(err error) {
	a.lastErr = err
	a.loading = false
}

// SetLoading marks a fetch as in flight.
func (a *App) SetLoading(v bool) { a.loading = v }

// Tick advances the spinner and re-renders durations against the new clock.
func (a *App) Tick(now time.Time) {
	a.spinner++
	if a.root != nil {
		a.rebuildPreservingCursor(now)
	}
}

func (a *App) rebuildPreservingCursor(now time.Time) {
	key := ""
	if n := a.Selected(); n != nil {
		key = n.Key
	}
	a.rebuild(now)
	a.restoreSelection(key)
}

func (a *App) rebuild(now time.Time) {
	a.now = now
	a.root = BuildTree(a.Repo, a.visibleBranch(), a.visiblePRs(), now)
	a.rows = Flatten(a.root, a.isExpanded)
	if a.sel >= len(a.rows) {
		a.sel = len(a.rows) - 1
	}
	if a.sel < 0 {
		a.sel = 0
	}
}

func (a *App) restoreSelection(key string) {
	if key == "" {
		return
	}
	for i, r := range a.rows {
		if r.Node.Key == key {
			a.sel = i
			return
		}
	}
}

// visibleBranch returns the default branch row, unless it is switched off. It
// deliberately ignores the pull request filters: "mine" and "drafts" are not
// properties a branch has, and hiding main because no pull request matches
// would lose the one row that says whether the repository itself is healthy.
func (a *App) visibleBranch() *Branch {
	if a.hideMain {
		return nil
	}
	return a.branch
}

func (a *App) visiblePRs() []PullRequest {
	out := make([]PullRequest, 0, len(a.prs))
	for _, pr := range a.prs {
		if a.hideDrafts && pr.Draft {
			continue
		}
		switch a.filter {
		case FilterAttention:
			st := pr.State()
			conflicting := strings.EqualFold(pr.Mergeable, "CONFLICTING")
			if st != StateFailure && st != StateCancelled && !conflicting {
				continue
			}
		case FilterRunning:
			if st := pr.State(); st != StateRunning && st != StatePending {
				continue
			}
		case FilterMine:
			if a.Viewer == "" || !strings.EqualFold(pr.Author, a.Viewer) {
				continue
			}
		}
		out = append(out, pr)
	}
	return out
}

// isExpanded answers for one node, consulting the user's explicit toggles
// first. The default matters: a passing workflow stays folded and a failing or
// running one opens itself, so the thing that needs reading is already open.
func (a *App) isExpanded(n *Node) bool {
	if v, ok := a.overrides[n.Key]; ok {
		return v
	}
	if a.autoCollapse && n.Kind != NodeRepo && len(n.Children) > 0 && leavesDone(n) {
		return false
	}
	switch n.Kind {
	case NodeRepo, NodeBranch, NodePR:
		return true
	case NodeWorkflow:
		return n.State == StateFailure || n.State == StateCancelled || n.State == StateRunning
	default:
		return false
	}
}

// leavesDone reports whether every leaf under n has reached a final state. A
// node with no children is its own leaf, and the "no checks reported"
// placeholder is never final, so a pull request without checks does not count.
func leavesDone(n *Node) bool {
	if len(n.Children) == 0 {
		return n.State.Terminal()
	}
	for _, c := range n.Children {
		if !leavesDone(c) {
			return false
		}
	}
	return true
}

// allChecksDone reports whether a row should get the "finished" highlight:
// every check has completed and none of them failed or was cancelled, so green
// never paints over a red result.
func allChecksDone(n *Node) bool {
	return n.Kind == NodePR && leavesDone(n) && n.State != StateFailure && n.State != StateCancelled
}

// highlightWindow is how long a pull request stays green after its last check
// finishes; the highlight is a notification, not a permanent state.
const highlightWindow = 30 * time.Second

// justFinished reports whether n should be highlighted at the clock of the last
// rebuild: all checks done, none red, and the last one finished recently.
func (a *App) justFinished(n *Node) bool {
	if !allChecksDone(n) || n.Finished.IsZero() {
		return false
	}
	age := a.now.Sub(n.Finished)
	return age >= 0 && age < highlightWindow
}

// Rows returns the currently visible rows.
func (a *App) Rows() []Row { return a.rows }

// Selected returns the node under the cursor, or nil when the tree is empty.
func (a *App) Selected() *Node {
	if a.sel < 0 || a.sel >= len(a.rows) {
		return nil
	}
	return a.rows[a.sel].Node
}

// SelectedIndex returns the cursor's row index.
func (a *App) SelectedIndex() int { return a.sel }

// Key applies a keypress. The argument is the key name produced by the
// terminal reader: a single character, or "up"/"down"/"left"/"right"/"enter"/
// "esc"/"pgup"/"pgdn"/"home"/"end".
func (a *App) Key(key string, now time.Time) Action {
	a.status = ""
	if a.showHelp {
		// Any key closes help except the ones that leave.
		switch key {
		case "q", "ctrl-c":
			return ActionQuit
		default:
			a.showHelp = false
			return ActionNone
		}
	}

	switch key {
	case "q", "ctrl-c", "esc":
		return ActionQuit
	case "?":
		a.showHelp = true
	case "r":
		return ActionRefresh
	case "o", "enter-open":
		return ActionOpen
	case "j", "down":
		a.move(1)
	case "k", "up":
		a.move(-1)
	case "g", "home":
		a.sel = 0
	case "G", "end":
		a.sel = len(a.rows) - 1
	case "ctrl-d", "pgdn":
		a.move(a.page())
	case "ctrl-u", "pgup":
		a.move(-a.page())
	case "J":
		a.jumpPR(1, now)
	case "K":
		a.jumpPR(-1, now)
	case "l", "right":
		a.setExpanded(true, now)
	case "h", "left":
		a.collapseOrAscend(now)
	case " ", "enter", "tab":
		a.toggle(now)
	case "e":
		a.expandAll(now)
	case "c":
		a.collapseAll(now)
	case "f":
		a.filter = a.filter.next()
		a.sel, a.top = 0, 0
		a.rebuild(now)
	case "d":
		a.hideDrafts = !a.hideDrafts
		a.rebuild(now)
		a.status = fmt.Sprintf("drafts %s", onOff(!a.hideDrafts))
	case "a":
		a.autoCollapse = !a.autoCollapse
		a.rebuildPreservingCursor(now)
		a.status = fmt.Sprintf("auto-collapse finished sections %s", onOffWord(a.autoCollapse))
	case "H":
		a.highlightDone = !a.highlightDone
		a.status = fmt.Sprintf("highlight just-finished pull requests %s", onOffWord(a.highlightDone))
	case "m":
		a.hideMain = !a.hideMain
		a.rebuild(now)
		a.status = fmt.Sprintf("default branch %s", onOff(!a.hideMain))
	}
	a.clampSelection()
	return ActionNone
}

func onOff(v bool) string {
	if v {
		return "shown"
	}
	return "hidden"
}

func onOffWord(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (a *App) page() int {
	if a.high > 2 {
		return a.high - 1
	}
	return 10
}

func (a *App) move(delta int) {
	a.sel += delta
	a.clampSelection()
}

func (a *App) clampSelection() {
	if a.sel < 0 {
		a.sel = 0
	}
	if a.sel >= len(a.rows) {
		a.sel = len(a.rows) - 1
	}
	if a.sel < 0 {
		a.sel = 0
	}
}

// jumpPR moves to the next or previous pull request row, skipping the checks
// under it -- the movement you want when scanning a long tree.
func (a *App) jumpPR(dir int, _ time.Time) {
	for i := a.sel + dir; i >= 0 && i < len(a.rows); i += dir {
		if a.rows[i].Node.Kind == NodePR {
			a.sel = i
			return
		}
	}
}

func (a *App) toggle(now time.Time) {
	n := a.Selected()
	if n == nil || len(n.Children) == 0 {
		return
	}
	a.overrides[n.Key] = !a.isExpanded(n)
	a.rebuildPreservingCursor(now)
}

func (a *App) setExpanded(v bool, now time.Time) {
	n := a.Selected()
	if n == nil || len(n.Children) == 0 {
		return
	}
	if a.isExpanded(n) == v {
		if v {
			a.move(1) // already open: step into it
		}
		return
	}
	a.overrides[n.Key] = v
	a.rebuildPreservingCursor(now)
}

// collapseOrAscend folds the selected node, or moves to its parent when it is
// already folded, which is how a file tree behaves.
func (a *App) collapseOrAscend(now time.Time) {
	n := a.Selected()
	if n == nil {
		return
	}
	if len(n.Children) > 0 && a.isExpanded(n) {
		a.overrides[n.Key] = false
		a.rebuildPreservingCursor(now)
		return
	}
	depth := a.rows[a.sel].Depth
	for i := a.sel - 1; i >= 0; i-- {
		if a.rows[i].Depth < depth {
			a.sel = i
			return
		}
	}
}

func (a *App) expandAll(now time.Time) {
	a.walkAll(func(n *Node) {
		if len(n.Children) > 0 {
			a.overrides[n.Key] = true
		}
	})
	a.rebuildPreservingCursor(now)
}

func (a *App) collapseAll(now time.Time) {
	a.walkAll(func(n *Node) {
		if n.Kind == NodeRepo {
			a.overrides[n.Key] = true
			return
		}
		if len(n.Children) > 0 {
			a.overrides[n.Key] = false
		}
	})
	a.rebuildPreservingCursor(now)
}

func (a *App) walkAll(fn func(*Node)) {
	if a.root == nil {
		return
	}
	var walk func(*Node)
	walk = func(n *Node) {
		fn(n)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(a.root)
}

// OpenURL returns the URL for the selected node: a check's run page, or the
// pull request it belongs to when the check has none.
func (a *App) OpenURL() string {
	n := a.Selected()
	if n == nil {
		return ""
	}
	if n.URL != "" {
		return n.URL
	}
	if n.PR != nil {
		return n.PR.URL
	}
	if n.Branch != nil {
		return n.Branch.URL
	}
	return ""
}
