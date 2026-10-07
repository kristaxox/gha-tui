package main

import (
	"bufio"
	"io"
	"unicode/utf8"
)

// decodeKey turns one keypress into the name internal/ghatui's key handler
// expects. It is split from the reader so escape-sequence decoding can be
// tested without a terminal: given the bytes already read, it returns the key
// name and how many bytes it consumed.
//
// A lone ESC is ambiguous -- it is both the escape key and the start of an
// arrow sequence -- so it resolves to "esc" only when no more bytes follow.
func decodeKey(buf []byte) (key string, n int) {
	if len(buf) == 0 {
		return "", 0
	}
	if buf[0] != 0x1b {
		return decodeSimple(buf)
	}
	if len(buf) == 1 {
		return "esc", 1
	}

	// CSI and SS3 sequences: ESC [ ... final, or ESC O final.
	if buf[1] == '[' || buf[1] == 'O' {
		if name, size, ok := decodeCSI(buf); ok {
			return name, size
		}
		// An incomplete sequence: wait for the rest.
		return "", 0
	}
	// ESC followed by anything else is alt+key, which nothing binds; drop it.
	return "", 2
}

func decodeSimple(buf []byte) (key string, n int) {
	switch buf[0] {
	case '\r', '\n':
		return "enter", 1
	case '\t':
		return "tab", 1
	case 0x03:
		return "ctrl-c", 1
	case 0x04:
		return "ctrl-d", 1
	case 0x15:
		return "ctrl-u", 1
	case 0x7f, 0x08:
		return "backspace", 1
	}
	if buf[0] < 0x20 {
		// Another control byte with no binding.
		return "", 1
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size <= 1 {
		if len(buf) < utf8.UTFMax {
			return "", 0 // possibly a partial rune; wait for more
		}
		return "", 1
	}
	return string(r), size
}

// decodeCSI handles ESC [ ... and ESC O ... sequences. ok is false when the
// buffer holds only part of one.
func decodeCSI(buf []byte) (key string, n int, ok bool) {
	// Find the final byte: the terminator of a CSI sequence is in @..~.
	end := -1
	for i := 2; i < len(buf); i++ {
		if buf[i] >= 0x40 && buf[i] <= 0x7e {
			end = i
			break
		}
	}
	if end < 0 {
		return "", 0, false
	}
	body := string(buf[2:end])
	final := buf[end]
	size := end + 1

	switch final {
	case 'A':
		return "up", size, true
	case 'B':
		return "down", size, true
	case 'C':
		return "right", size, true
	case 'D':
		return "left", size, true
	case 'H':
		return "home", size, true
	case 'F':
		return "end", size, true
	case '~':
		switch body {
		case "1", "7":
			return "home", size, true
		case "4", "8":
			return "end", size, true
		case "5":
			return "pgup", size, true
		case "6":
			return "pgdn", size, true
		}
		return "", size, true
	}
	return "", size, true
}

// readKeys decodes keypresses from r and sends their names on out until r is
// exhausted or the channel's consumer goes away. It owns no terminal state; the
// caller supplies the already-raw tty.
func readKeys(r io.Reader, out chan<- string, done <-chan struct{}) {
	defer close(out)
	reader := bufio.NewReader(r)
	var buf []byte
	chunk := make([]byte, 64)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			for len(buf) > 0 {
				key, consumed := decodeKey(buf)
				if consumed == 0 {
					break // partial sequence: wait for more bytes
				}
				buf = buf[consumed:]
				if key == "" {
					continue
				}
				select {
				case out <- key:
				case <-done:
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}
