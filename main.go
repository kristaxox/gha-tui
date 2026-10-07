// Command gha-tui draws a GitHub repository's open pull requests and the
// GitHub Actions checks running on them as a live, navigable tree.
//
// Everything in this file is the IO the rest of the program avoids: flags, the
// terminal's raw mode and alternate screen, the tickers, signal handling and
// the browser launch. The model, tree, key handling and rendering live in
// internal/ghatui as pure functions, which is what makes them testable without
// a network or a TTY.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kristaxox/gha-tui/internal/ghatui"
)

// fetchTimeout bounds a single refresh so a hung gh call cannot wedge the loop.
const fetchTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gha-tui:", err)
		os.Exit(1)
	}
}

type options struct {
	repo     string
	interval time.Duration
	limit    int
	once     bool
	noColor  bool
	ascii    bool
	noMain   bool
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("gha-tui", flag.ContinueOnError)
	fs.StringVar(&o.repo, "repo", "", "repository to watch, as OWNER/NAME (default: the current checkout)")
	fs.DurationVar(&o.interval, "interval", 30*time.Second, "auto-refresh interval; 0 disables auto-refresh")
	fs.IntVar(&o.limit, "limit", 30, "maximum number of open pull requests to fetch")
	fs.BoolVar(&o.once, "once", false, "print the tree once, without cursor or chrome, and exit")
	fs.BoolVar(&o.noColor, "no-color", false, "disable colour (NO_COLOR is honoured too)")
	fs.BoolVar(&o.ascii, "ascii", false, "use ASCII glyphs instead of box drawing and braille")
	fs.BoolVar(&o.noMain, "no-main", false, "omit the default branch's own checks from the tree")
	fs.Usage = func() {
		out := fs.Output()
		_, _ = fmt.Fprintln(out, "usage: gha-tui [flags]")
		_, _ = fmt.Fprintln(out, "\nWatch a repository's open pull requests and their GitHub Actions checks.")
		_, _ = fmt.Fprintln(out, "\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.limit <= 0 {
		return o, fmt.Errorf("--limit must be positive, got %d", o.limit)
	}
	if o.interval < 0 {
		return o, fmt.Errorf("--interval must not be negative, got %s", o.interval)
	}
	return o, nil
}

func run() error {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := ghatui.ExecRunner{}
	repo, err := resolveRepo(ctx, runner, opts.repo)
	if err != nil {
		return err
	}

	theme := ghatui.Theme{
		Color: !opts.noColor && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		ASCII: opts.ascii,
	}

	app := ghatui.NewApp(repo)
	app.HideMain(opts.noMain)
	if opts.once {
		theme.Color = theme.Color && isTerminal(os.Stdout)
		return once(ctx, runner, app, theme, opts)
	}
	app.Viewer = viewerLogin(ctx, runner)
	return interactive(ctx, runner, app, theme, opts)
}

// resolveRepo prefers the flag, then the repository the working directory
// belongs to. Outside a GitHub checkout with no flag there is nothing to watch.
func resolveRepo(ctx context.Context, r ghatui.Runner, flagRepo string) (string, error) {
	if strings.TrimSpace(flagRepo) != "" {
		return strings.TrimSpace(flagRepo), nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	if repo, err := ghatui.CurrentRepo(lookupCtx, r); err == nil {
		return repo, nil
	}
	return "", errors.New("not in a GitHub checkout; pass --repo OWNER/NAME")
}

// viewerLogin resolves the authenticated user, for the "mine" filter. An empty
// result just makes that filter show nothing, so the error is not fatal.
func viewerLogin(ctx context.Context, r ghatui.Runner) string {
	lookupCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	out, err := r.Run(lookupCtx, "api", "user", "--jq", ".login")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// once prints the tree a single time. It is the mode a script or another agent
// reads, so it writes to stdout and reports a fetch failure as an error rather
// than drawing it on a status line.
func once(ctx context.Context, r ghatui.Runner, app *ghatui.App, theme ghatui.Theme, opts options) error {
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	prs, branch, err := ghatui.Fetch(fetchCtx, r, app.Repo, opts.limit)
	if err != nil {
		return err
	}
	app.SetData(prs, branch, time.Now())

	width, _, err := terminalSize()
	if err != nil || width <= 0 {
		width = 100
	}
	for _, line := range app.Plain(theme, width) {
		fmt.Println(line)
	}
	return nil
}
