package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/kristaxox/gha-tui/internal/ghatui"
)

// spinnerInterval is how often the spinner advances and durations are
// recomputed. It refetches nothing; it only re-renders against a newer clock.
const spinnerInterval = time.Second

// fetchResult carries a refresh back from its goroutine.
type fetchResult struct {
	prs    []ghatui.PullRequest
	branch *ghatui.Branch
	err    error
}

// interactive runs the full-screen loop until the user quits or a signal
// arrives. The deferred restore is what guarantees the terminal comes back,
// including on panic; SIGINT and SIGTERM arrive through ctx.
func interactive(ctx context.Context, r ghatui.Runner, app *ghatui.App, theme ghatui.Theme, opts options) (err error) {
	term, err := newTerminal()
	if err != nil {
		return err
	}
	// Runs on a normal return, on a panic, and after the loop exits on a
	// signal. restore is idempotent, so the repeated paths are harmless.
	defer term.restore()

	done := make(chan struct{})
	defer close(done)

	keys := make(chan string, 8)
	go readKeys(term.tty, keys, done)

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)

	results := make(chan fetchResult, 1)
	refresh := func() {
		app.SetLoading(true)
		go func() {
			fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
			prs, branch, err := ghatui.Fetch(fetchCtx, r, app.Repo, opts.limit)
			select {
			case results <- fetchResult{prs: prs, branch: branch, err: err}:
			case <-done:
			}
		}()
	}

	var refreshTick <-chan time.Time
	if opts.interval > 0 {
		ticker := time.NewTicker(opts.interval)
		defer ticker.Stop()
		refreshTick = ticker.C
	}
	spinner := time.NewTicker(spinnerInterval)
	defer spinner.Stop()

	width, height := term.size()
	redraw := func() {
		term.draw(app.Render(theme, width, height, time.Now()))
	}

	refresh()
	redraw()

	for {
		select {
		case <-ctx.Done():
			// SIGINT or SIGTERM: the deferred restore puts the terminal back.
			return nil

		case res := <-results:
			if res.err != nil {
				// A failed refresh keeps the last good tree on screen and puts
				// the reason on the status line.
				app.SetError(res.err)
			} else {
				app.SetData(res.prs, res.branch, time.Now())
			}
			redraw()

		case <-refreshTick:
			refresh()
			redraw()

		case <-spinner.C:
			app.Tick(time.Now())
			redraw()

		case <-resize:
			width, height = term.size()
			redraw()

		case key, ok := <-keys:
			if !ok {
				return nil // stdin closed
			}
			switch app.Key(key, time.Now()) {
			case ghatui.ActionQuit:
				return nil
			case ghatui.ActionRefresh:
				refresh()
			case ghatui.ActionOpen:
				if url := app.OpenURL(); url != "" {
					openBrowser(ctx, url)
				}
			}
			redraw()
		}
	}
}

// openBrowser opens a URL with the platform's handler, preferring `gh browse`'s
// underlying behaviour via the OS opener. Failure is deliberately silent: the
// tree is still on screen and an error popup would obscure it.
func openBrowser(ctx context.Context, url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// #nosec G204 -- url comes from the GitHub API response the tree was
		// built from, not from user input on the command line.
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		// #nosec G204 -- see above.
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		// #nosec G204 -- see above.
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return
	}
	// Reap the child without blocking the loop.
	go func() { _ = cmd.Wait() }()
}
