package ghatui

import (
	"fmt"
	"strings"
	"time"
)

// NodeKind is the level a node sits at in the tree.
type NodeKind int

const (
	// NodeRepo is the single root: the repository being watched.
	NodeRepo NodeKind = iota
	// NodeBranch is the repository's default branch and the checks on its
	// head commit.
	NodeBranch
	// NodePR is one open pull request.
	NodePR
	// NodeWorkflow is one workflow's checks within a pull request.
	NodeWorkflow
	// NodeCheck is one job or commit status.
	NodeCheck
)

// A Node is one line of the tree.
type Node struct {
	Kind     NodeKind
	Key      string // stable across refreshes; expansion state is keyed on it
	Label    string
	Detail   string
	State    State
	URL      string
	Finished time.Time    // latest check completion, set on NodePR
	PR       *PullRequest // set on NodePR and below, for actions like "open"
	Branch   *Branch      // set on NodeBranch and below
	Children []*Node
}

// BuildTree assembles the repository -> pull request -> workflow -> check tree.
// now is passed in rather than read from the clock so the durations a test
// asserts do not move underneath it.
//
// branch, when non-nil, becomes the first child: the default branch's own
// checks, above the pull requests proposing to change it.
func BuildTree(repo string, branch *Branch, prs []PullRequest, now time.Time) *Node {
	root := &Node{Kind: NodeRepo, Key: "repo", Label: repo}

	if branch != nil {
		root.Children = append(root.Children, branchNode(branch, now))
	}

	failing, running := 0, 0
	for i := range prs {
		pr := &prs[i]
		prState := pr.State()
		switch prState {
		case StateFailure, StateCancelled:
			failing++
		case StateRunning, StatePending:
			running++
		}

		passed, total := pr.Counts()
		prNode := &Node{
			Kind:   NodePR,
			Key:    fmt.Sprintf("pr:%d", pr.Number),
			Label:  fmt.Sprintf("#%d %s", pr.Number, pr.Title),
			Detail: prDetail(pr, passed, total, now),
			State:  prState,
			URL:    pr.URL,
			PR:     pr,
		}
		for _, c := range pr.Checks {
			if c.CompletedAt.After(prNode.Finished) {
				prNode.Finished = c.CompletedAt
			}
		}

		for _, g := range groupByWorkflow(pr.Checks) {
			gPassed, gTotal := g.counts()
			wfNode := &Node{
				Kind:   NodeWorkflow,
				Key:    fmt.Sprintf("pr:%d/wf:%s", pr.Number, g.Name),
				Label:  g.Name,
				Detail: fmt.Sprintf("%d/%d %s", gPassed, gTotal, groupDuration(g, now)),
				State:  g.state(),
				PR:     pr,
			}
			for _, c := range g.Checks {
				wfNode.Children = append(wfNode.Children, &Node{
					Kind:   NodeCheck,
					Key:    fmt.Sprintf("pr:%d/wf:%s/check:%s", pr.Number, g.Name, c.Name),
					Label:  c.Name,
					Detail: checkDetail(c, now),
					State:  c.State,
					URL:    c.URL,
					PR:     pr,
				})
			}
			prNode.Children = append(prNode.Children, wfNode)
		}

		if total == 0 {
			prNode.Children = append(prNode.Children, &Node{
				Kind:   NodeCheck,
				Key:    fmt.Sprintf("pr:%d/nochecks", pr.Number),
				Label:  "no checks reported",
				State:  StateUnknown,
				PR:     pr,
				Detail: "",
			})
		}

		root.Children = append(root.Children, prNode)
	}

	root.State = rollNodes(root.Children)
	root.Detail = repoDetail(len(prs), failing, running)
	return root
}

// branchNode builds the default branch's subtree, which has the same
// workflow -> check shape as a pull request's.
func branchNode(b *Branch, now time.Time) *Node {
	passed, total := b.Counts()
	node := &Node{
		Kind:   NodeBranch,
		Key:    "branch:" + b.Name,
		Label:  b.Name,
		Detail: branchDetail(b, passed, total, now),
		State:  b.State(),
		URL:    b.URL,
		Branch: b,
	}
	for _, g := range groupByWorkflow(b.Checks) {
		gPassed, gTotal := g.counts()
		wf := &Node{
			Kind:   NodeWorkflow,
			Key:    fmt.Sprintf("branch:%s/wf:%s", b.Name, g.Name),
			Label:  g.Name,
			Detail: fmt.Sprintf("%d/%d %s", gPassed, gTotal, groupDuration(g, now)),
			State:  g.state(),
			Branch: b,
		}
		for _, c := range g.Checks {
			wf.Children = append(wf.Children, &Node{
				Kind:   NodeCheck,
				Key:    fmt.Sprintf("branch:%s/wf:%s/check:%s", b.Name, g.Name, c.Name),
				Label:  c.Name,
				Detail: checkDetail(c, now),
				State:  c.State,
				URL:    c.URL,
				Branch: b,
			})
		}
		node.Children = append(node.Children, wf)
	}
	if total == 0 {
		node.Children = append(node.Children, &Node{
			Kind:   NodeCheck,
			Key:    "branch:" + b.Name + "/nochecks",
			Label:  "no checks reported",
			State:  StateUnknown,
			Branch: b,
		})
	}
	return node
}

// branchDetail is the right-hand column for the branch row. The commit hash
// is not here: it sits next to the branch name, where it reads as an identity
// rather than as another statistic.
func branchDetail(b *Branch, passed, total int, now time.Time) string {
	parts := []string{}
	if total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", passed, total))
	} else {
		parts = append(parts, "no checks")
	}
	if !b.Committed.IsZero() {
		parts = append(parts, Ago(b.Committed, now))
	}
	return strings.Join(parts, " · ")
}

func rollNodes(nodes []*Node) State {
	states := make([]State, 0, len(nodes))
	for _, n := range nodes {
		states = append(states, n.State)
	}
	return Roll(states)
}

func repoDetail(open, failing, running int) string {
	parts := []string{fmt.Sprintf("%d open", open)}
	if failing > 0 {
		parts = append(parts, fmt.Sprintf("%d failing", failing))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d in flight", running))
	}
	return strings.Join(parts, " · ")
}

func prDetail(pr *PullRequest, passed, total int, now time.Time) string {
	parts := []string{}
	if pr.Author != "" {
		parts = append(parts, pr.Author)
	}
	if total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", passed, total))
	} else {
		parts = append(parts, "no checks")
	}
	if r := reviewWord(pr.ReviewDecision); r != "" {
		parts = append(parts, r)
	}
	if strings.EqualFold(pr.Mergeable, "CONFLICTING") {
		parts = append(parts, "conflicts")
	}
	if !pr.UpdatedAt.IsZero() {
		parts = append(parts, Ago(pr.UpdatedAt, now))
	}
	return strings.Join(parts, " · ")
}

func reviewWord(decision string) string {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "REVIEW_REQUIRED":
		return "review required"
	}
	return ""
}

func groupDuration(g workflowGroup, now time.Time) string {
	var start, end time.Time
	for _, c := range g.Checks {
		if c.StartedAt.IsZero() {
			continue
		}
		if start.IsZero() || c.StartedAt.Before(start) {
			start = c.StartedAt
		}
		finish := c.CompletedAt
		if finish.IsZero() {
			finish = now
		}
		if finish.After(end) {
			end = finish
		}
	}
	if start.IsZero() || !end.After(start) {
		return ""
	}
	return Dur(end.Sub(start))
}

func checkDetail(c Check, now time.Time) string {
	d := c.Duration(now)
	switch {
	case c.State == StatePending:
		return "queued"
	case d <= 0:
		return ""
	default:
		return Dur(d)
	}
}

// A Row is one visible line: a node plus the box-drawing prefix that places it
// under its parent.
type Row struct {
	Node     *Node
	Prefix   string
	Depth    int
	Expanded bool
}

// HasChildren reports whether the row can be expanded at all.
func (r Row) HasChildren() bool { return len(r.Node.Children) > 0 }

// Flatten walks the tree in display order, descending only into nodes that
// isExpanded accepts, and returns the rows to draw.
func Flatten(root *Node, isExpanded func(*Node) bool) []Row {
	var rows []Row
	var walk func(n *Node, prefix string, depth int, last bool, top bool)
	walk = func(n *Node, prefix string, depth int, last bool, top bool) {
		linePrefix := prefix
		if !top {
			if last {
				linePrefix = prefix + "└─ "
			} else {
				linePrefix = prefix + "├─ "
			}
		}
		expanded := isExpanded(n)
		rows = append(rows, Row{Node: n, Prefix: linePrefix, Depth: depth, Expanded: expanded})
		if !expanded {
			return
		}
		childPrefix := prefix
		if !top {
			if last {
				childPrefix = prefix + "   "
			} else {
				childPrefix = prefix + "│  "
			}
		}
		for i, c := range n.Children {
			walk(c, childPrefix, depth+1, i == len(n.Children)-1, false)
		}
	}
	walk(root, "", 0, true, true)
	return rows
}

// Dur formats a check duration the way CI logs do: 45s, 4m12s, 1h02m.
func Dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Ago formats how long ago t was, for the "updated" column.
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
