package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestDecodeKeySimple(t *testing.T) {
	tests := []struct {
		in   string
		key  string
		size int
	}{
		{"j", "j", 1},
		{"J", "J", 1},
		{"?", "?", 1},
		{" ", " ", 1},
		{"\r", "enter", 1},
		{"\n", "enter", 1},
		{"\t", "tab", 1},
		{"\x03", "ctrl-c", 1},
		{"\x04", "ctrl-d", 1},
		{"\x15", "ctrl-u", 1},
		{"é", "é", 2}, // a multi-byte rune is one key
	}
	for _, tt := range tests {
		key, n := decodeKey([]byte(tt.in))
		if key != tt.key || n != tt.size {
			t.Errorf("decodeKey(%q) = (%q,%d), want (%q,%d)", tt.in, key, n, tt.key, tt.size)
		}
	}
}

func TestDecodeKeyEscapeSequences(t *testing.T) {
	tests := []struct {
		in   string
		key  string
		size int
	}{
		{"\x1b[A", "up", 3},
		{"\x1b[B", "down", 3},
		{"\x1b[C", "right", 3},
		{"\x1b[D", "left", 3},
		{"\x1b[H", "home", 3},
		{"\x1b[F", "end", 3},
		{"\x1bOA", "up", 3}, // SS3, what some terminals send in application mode
		{"\x1b[5~", "pgup", 4},
		{"\x1b[6~", "pgdn", 4},
		{"\x1b[1~", "home", 4},
		{"\x1b[4~", "end", 4},
		{"\x1b[1;5A", "up", 6}, // modified arrow: the modifier is ignored
	}
	for _, tt := range tests {
		key, n := decodeKey([]byte(tt.in))
		if key != tt.key || n != tt.size {
			t.Errorf("decodeKey(%q) = (%q,%d), want (%q,%d)", tt.in, key, n, tt.key, tt.size)
		}
	}
}

// A lone ESC is the escape key; ESC followed by more bytes is a sequence. The
// decoder must not mistake a half-read arrow for a quit.
func TestDecodeKeyLoneEscape(t *testing.T) {
	if key, n := decodeKey([]byte{0x1b}); key != "esc" || n != 1 {
		t.Errorf("lone ESC = (%q,%d), want (esc,1)", key, n)
	}
	// An incomplete sequence consumes nothing and waits for the rest.
	if key, n := decodeKey([]byte("\x1b[")); key != "" || n != 0 {
		t.Errorf("partial CSI = (%q,%d), want (\"\",0)", key, n)
	}
	if key, n := decodeKey([]byte("\x1b[1;")); key != "" || n != 0 {
		t.Errorf("partial parameterised CSI = (%q,%d), want (\"\",0)", key, n)
	}
}

func TestDecodeKeyEmpty(t *testing.T) {
	if key, n := decodeKey(nil); key != "" || n != 0 {
		t.Errorf("empty = (%q,%d)", key, n)
	}
}

// Unbound sequences must be consumed rather than left to jam the buffer.
func TestDecodeKeyConsumesUnknownSequences(t *testing.T) {
	key, n := decodeKey([]byte("\x1b[3~")) // delete
	if key != "" {
		t.Errorf("delete decoded as %q, want no key", key)
	}
	if n != 4 {
		t.Errorf("delete consumed %d bytes, want 4", n)
	}
}

func TestReadKeysStream(t *testing.T) {
	// A single read carrying several keys, including a split-free arrow.
	input := strings.NewReader("jk\x1b[Bq")
	out := make(chan string, 16)
	done := make(chan struct{})
	defer close(done)

	go readKeys(input, out, done)

	var got []string
	for key := range out {
		got = append(got, key)
	}
	want := []string{"j", "k", "down", "q"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// An escape sequence split across two reads must still decode as one key.
func TestReadKeysSplitSequence(t *testing.T) {
	pr, pw := io.Pipe()
	out := make(chan string, 8)
	done := make(chan struct{})
	defer close(done)
	go readKeys(pr, out, done)

	go func() {
		_, _ = pw.Write([]byte("\x1b["))
		time.Sleep(10 * time.Millisecond)
		_, _ = pw.Write([]byte("A"))
		_ = pw.Close()
	}()

	select {
	case key := <-out:
		if key != "up" {
			t.Errorf("split sequence = %q, want up", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the decoded key")
	}
}

// readKeys must exit when done closes, even though its consumer has stopped
// draining -- otherwise every quit would leak the reader goroutine.
func TestReadKeysStopsOnDone(t *testing.T) {
	pr, pw := io.Pipe()
	out := make(chan string)
	done := make(chan struct{})

	// readKeys closes out on the way out, so a closed channel is the signal
	// that the goroutine actually returned.
	stopped := make(chan struct{})
	go func() {
		readKeys(pr, out, done)
		close(stopped)
	}()

	go func() {
		for {
			if _, err := pw.Write([]byte("jjjj")); err != nil {
				return
			}
		}
	}()

	<-out // one key through, then stop draining so the send blocks
	close(done)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("readKeys did not stop when done was closed")
	}
	_ = pw.Close()
}
