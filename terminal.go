package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// ANSI control sequences. Written out rather than pulled from a library to
// keep the binary dependency-free.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	cursorHome   = "\x1b[H"
	clearScreen  = "\x1b[2J"
	clearLine    = "\x1b[K"
	sgrReset     = "\x1b[0m"
)

// terminal owns the tty's mode and the alternate screen. Every path out of the
// program -- a normal quit, a signal, a panic -- goes through restore, because
// a terminal left in raw mode with a hidden cursor is a broken shell.
type terminal struct {
	tty      *os.File
	saved    string
	restored sync.Once
}

// newTerminal puts the terminal into raw mode on the alternate screen, saving
// the previous stty state so restore can put it back exactly.
func newTerminal() (*terminal, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("opening /dev/tty: %w", err)
	}
	saved, err := stty(tty, "-g")
	if err != nil {
		_ = tty.Close()
		return nil, err
	}
	if _, err := stty(tty, "raw", "-echo"); err != nil {
		_ = tty.Close()
		return nil, err
	}
	t := &terminal{tty: tty, saved: strings.TrimSpace(saved)}
	t.write(altScreenOn + cursorHide + clearScreen + cursorHome)
	return t, nil
}

// restore returns the terminal to the state it was in. It is safe to call more
// than once, which matters because the deferred call, the signal path and the
// panic path can all reach it.
func (t *terminal) restore() {
	t.restored.Do(func() {
		t.write(sgrReset + cursorShow + altScreenOff)
		if t.saved != "" {
			_, _ = stty(t.tty, t.saved)
		}
		_ = t.tty.Close()
	})
}

func (t *terminal) write(s string) {
	_, _ = t.tty.WriteString(s)
}

// draw paints the frame. Each line is cleared to the right rather than the
// whole screen being erased first, so a redraw does not flicker.
func (t *terminal) draw(lines []string) {
	var b strings.Builder
	b.WriteString(cursorHome)
	for i, line := range lines {
		b.WriteString(line)
		b.WriteString(clearLine)
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J") // clear anything below a shorter frame
	t.write(b.String())
}

// stty shells out to stty against the tty. Reading the size and setting raw
// mode this way avoids a golang.org/x/term dependency, which is the whole
// point of the zero-dependency constraint.
func stty(tty *os.File, args ...string) (string, error) {
	// #nosec G204 -- args are fixed strings from this file plus the state
	// string stty itself produced; none of it comes from remote input.
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if msg != "" {
				return "", fmt.Errorf("stty %s: %s", strings.Join(args, " "), msg)
			}
		}
		return "", fmt.Errorf("stty %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// terminalSize reports the terminal's width and height in cells.
func terminalSize() (width, height int, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tty.Close() }()
	return sizeFrom(tty)
}

func sizeFrom(tty *os.File) (width, height int, err error) {
	out, err := stty(tty, "size")
	if err != nil {
		return 0, 0, err
	}
	return parseSize(out)
}

// parseSize reads `stty size` output, which is "rows cols".
func parseSize(out string) (width, height int, err error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected stty size output %q", strings.TrimSpace(out))
	}
	rows, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing rows from stty size: %w", err)
	}
	cols, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing columns from stty size: %w", err)
	}
	return cols, rows, nil
}

// size reports the terminal's current size, falling back to a usable default
// when stty cannot answer.
func (t *terminal) size() (width, height int) {
	w, h, err := sizeFrom(t.tty)
	if err != nil || w <= 0 || h <= 0 {
		return 100, 30
	}
	return w, h
}

// isTerminal reports whether f is a character device, used to decide whether
// --once should colour its output.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
