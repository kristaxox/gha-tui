package ghatui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs a `gh` invocation and returns its stdout. The interface exists so
// the fetch path can be tested against recorded JSON; the real implementation
// is ExecRunner.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner runs the `gh` CLI found on PATH. Authentication, host selection
// and rate limiting are all gh's problem, which is the point of shelling out
// rather than holding a token here.
type ExecRunner struct {
	// Dir, when set, is the working directory gh runs in. It matters because
	// `gh repo view` resolves the repository from the git remote.
	Dir string
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	// #nosec G204 -- args are built in this package from flags and a fixed
	// query; nothing here comes from a request or a remote document.
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		msg := strings.TrimSpace(stderr.String())
		if errors.As(err, &exitErr) && msg != "" {
			return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// CurrentRepo asks gh which repository the working directory belongs to, in
// OWNER/NAME form.
func CurrentRepo(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(ctx, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	repo := strings.TrimSpace(string(out))
	if repo == "" {
		return "", errors.New("gh repo view returned no repository")
	}
	return repo, nil
}

// SplitRepo splits OWNER/NAME.
func SplitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repository %q is not in OWNER/NAME form", repo)
	}
	return owner, name, nil
}

// prQuery asks for the open pull requests and, for each, the check rollup on
// its head commit. One GraphQL round trip covers the whole tree; the REST
// equivalent is a request per pull request plus one per check suite, which is
// what makes an auto-refreshing view worth avoiding.
//
// It also asks for the default branch's head commit, so the tree can answer
// "is main itself green?" -- the question a red pull request always raises.
// The rollup selection is identical for both, so it lives in a fragment.
const prQuery = `
fragment rollup on Commit {
  oid
  messageHeadline
  committedDate
  statusCheckRollup {
    contexts(first:100) {
      nodes {
        __typename
        ... on CheckRun {
          name
          status
          conclusion
          startedAt
          completedAt
          detailsUrl
          checkSuite { workflowRun { workflow { name } } }
        }
        ... on StatusContext {
          context
          state
          targetUrl
          createdAt
        }
      }
    }
  }
}

query($owner:String!, $name:String!, $limit:Int!) {
  repository(owner:$owner, name:$name) {
    defaultBranchRef {
      name
      target {
        ... on Commit {
          ...rollup
          url
        }
      }
    }
    pullRequests(states:OPEN, first:$limit, orderBy:{field:UPDATED_AT, direction:DESC}) {
      nodes {
        number
        title
        url
        isDraft
        updatedAt
        headRefName
        reviewDecision
        mergeable
        author { login }
        commits(last:1) {
          nodes {
            commit { ...rollup }
          }
        }
      }
    }
  }
}`

type graphQLResponse struct {
	Data struct {
		Repository struct {
			DefaultBranchRef *struct {
				Name   string     `json:"name"`
				Target commitNode `json:"target"`
			} `json:"defaultBranchRef"`
			PullRequests struct {
				Nodes []prNode `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// commitNode is the rollup fragment: the same selection for a pull request's
// head commit and for the default branch's.
type commitNode struct {
	OID               string    `json:"oid"`
	MessageHeadline   string    `json:"messageHeadline"`
	CommittedDate     time.Time `json:"committedDate"`
	URL               string    `json:"url"`
	StatusCheckRollup *struct {
		Contexts struct {
			Nodes []contextNode `json:"nodes"`
		} `json:"contexts"`
	} `json:"statusCheckRollup"`
}

// checks converts the rollup contexts, which is the only thing both callers
// want from a commit.
func (c commitNode) checks() []Check {
	if c.StatusCheckRollup == nil {
		return nil
	}
	out := make([]Check, 0, len(c.StatusCheckRollup.Contexts.Nodes))
	for _, ctx := range c.StatusCheckRollup.Contexts.Nodes {
		out = append(out, ctx.toCheck())
	}
	return out
}

type prNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	IsDraft        bool      `json:"isDraft"`
	UpdatedAt      time.Time `json:"updatedAt"`
	HeadRefName    string    `json:"headRefName"`
	ReviewDecision string    `json:"reviewDecision"`
	Mergeable      string    `json:"mergeable"`
	Author         struct {
		Login string `json:"login"`
	} `json:"author"`
	Commits struct {
		Nodes []struct {
			Commit commitNode `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type contextNode struct {
	TypeName string `json:"__typename"`

	// CheckRun
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	DetailsURL  string     `json:"detailsUrl"`
	CheckSuite  struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`

	// StatusContext
	Context   string     `json:"context"`
	State     string     `json:"state"`
	TargetURL string     `json:"targetUrl"`
	CreatedAt *time.Time `json:"createdAt"`
}

// Fetch returns the open pull requests of repo, newest activity first, and the
// state of its default branch. One round trip covers both.
func Fetch(ctx context.Context, r Runner, repo string, limit int) ([]PullRequest, *Branch, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	out, err := r.Run(ctx, "api", "graphql",
		"-f", "query="+prQuery,
		"-F", "owner="+owner,
		"-F", "name="+name,
		"-F", "limit="+strconv.Itoa(limit),
	)
	if err != nil {
		return nil, nil, err
	}
	return parse(out)
}

// parse decodes a whole response. The branch is nil for a repository with no
// commits yet, which is the one case defaultBranchRef comes back null.
func parse(data []byte) ([]PullRequest, *Branch, error) {
	prs, err := parsePRs(data)
	if err != nil {
		return nil, nil, err
	}
	branch, err := parseBranch(data)
	if err != nil {
		return nil, nil, err
	}
	return prs, branch, nil
}

func parseBranch(data []byte) (*Branch, error) {
	var resp graphQLResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decoding gh api graphql output: %w", err)
	}
	ref := resp.Data.Repository.DefaultBranchRef
	if ref == nil || ref.Name == "" {
		return nil, nil
	}
	return &Branch{
		Name:      ref.Name,
		SHA:       ref.Target.OID,
		Headline:  ref.Target.MessageHeadline,
		URL:       ref.Target.URL,
		Committed: ref.Target.CommittedDate,
		Checks:    ref.Target.checks(),
	}, nil
}

func parsePRs(data []byte) ([]PullRequest, error) {
	var resp graphQLResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decoding gh api graphql output: %w", err)
	}
	nodes := resp.Data.Repository.PullRequests.Nodes
	prs := make([]PullRequest, 0, len(nodes))
	for _, n := range nodes {
		pr := PullRequest{
			Number:         n.Number,
			Title:          n.Title,
			Author:         n.Author.Login,
			Branch:         n.HeadRefName,
			URL:            n.URL,
			Draft:          n.IsDraft,
			ReviewDecision: n.ReviewDecision,
			Mergeable:      n.Mergeable,
			UpdatedAt:      n.UpdatedAt,
		}
		if len(n.Commits.Nodes) > 0 {
			commit := n.Commits.Nodes[0].Commit
			pr.HeadSHA = commit.OID
			pr.Checks = commit.checks()
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func (c contextNode) toCheck() Check {
	if c.TypeName == "StatusContext" {
		check := Check{
			Name:     c.Context,
			Workflow: "status checks",
			State:    statusState(c.State),
			URL:      c.TargetURL,
		}
		if c.CreatedAt != nil {
			check.StartedAt = *c.CreatedAt
		}
		// A commit status is a point in time, not an interval.
		check.CompletedAt = check.StartedAt
		return check
	}

	workflow := "other"
	if c.CheckSuite.WorkflowRun != nil && c.CheckSuite.WorkflowRun.Workflow.Name != "" {
		workflow = c.CheckSuite.WorkflowRun.Workflow.Name
	}
	check := Check{
		Name:     c.Name,
		Workflow: workflow,
		State:    checkState(c.Status, c.Conclusion),
		URL:      c.DetailsURL,
	}
	if c.StartedAt != nil {
		check.StartedAt = *c.StartedAt
	}
	if c.CompletedAt != nil {
		check.CompletedAt = *c.CompletedAt
	}
	return check
}
