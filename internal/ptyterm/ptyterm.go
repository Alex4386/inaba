// Package ptyterm runs a command on a pseudo-terminal and keeps its recent output, for
// any number of viewers to attach to, as they come and go.
package ptyterm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// How much output a late viewer still gets to see.
const scrollback = 256 << 10

// The most one send carries: a stream message is read only once all of it has arrived.
const maxSend = 32 << 10

// ErrExited is what writing to or resizing a terminal whose process ended gives.
var ErrExited = errors.New("the terminal's process has exited")

// pseudo is a pseudo-terminal with a process on it.
type pseudo interface {
	io.ReadWriter
	Resize(cols, rows uint16) error
	// End ends the process, and with it what Read gives.
	End()
	// Wait lets go of the process, once Read has come to its end.
	Wait()
}

type Terminal struct {
	pty pseudo

	mu      sync.Mutex
	buf     []byte
	base    int64 // stream offset of buf[0]
	done    bool
	changed chan struct{}
	// The size last set, for viewers who draw at it rather than setting their own.
	cols, rows uint16
	// The window title the program last set, and an escape sequence for it cut off at the end of a read.
	title   string
	partial []byte
}

// Start runs argv in dir on a new PTY.
// Start runs argv in dir on a terminal of cols by rows; zero for either means 80 by 24.
// env is added to this process's own, as a program's settings.
func Start(argv []string, dir string, cols, rows uint16, env ...string) (*Terminal, error) {
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	p, err := startPTY(argv, dir, append(append(os.Environ(), env...), "TERM=xterm-256color"), cols, rows)
	if err != nil {
		return nil, err
	}
	t := &Terminal{pty: p, changed: make(chan struct{}), cols: cols, rows: rows}
	go t.read()
	return t, nil
}

// Exited stands for a terminal whose process ended before this one started: nothing
// to read or write, waiting to be started again.
func Exited() *Terminal {
	return &Terminal{done: true, changed: make(chan struct{})}
}

func (t *Terminal) read() {
	chunk := make([]byte, 32<<10)
	for {
		n, err := t.pty.Read(chunk)
		t.mu.Lock()
		t.buf = append(t.buf, chunk[:n]...)
		if title, ok, partial := lastTitle(append(t.partial, chunk[:n]...)); ok {
			t.title, t.partial = title, partial
		} else {
			t.partial = partial
		}
		if over := len(t.buf) - scrollback; over > 0 {
			t.buf = t.buf[over:]
			t.base += int64(over)
		}
		t.done = err != nil
		close(t.changed)
		t.changed = make(chan struct{})
		t.mu.Unlock()
		if err != nil {
			t.pty.Wait()
			return
		}
	}
}

// Title is the window title the program on the terminal last set, as a shell's prompt does
// with user@host:folder; empty until it sets one.
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// The escape sequences that set the window title: OSC 0 or OSC 2, ended by BEL or ST.
var titleStarts = [][]byte{[]byte("\x1b]0;"), []byte("\x1b]2;")}

// maxTitle bounds a title, and with it how much of a sequence cut off by a read is kept.
const maxTitle = 1 << 10

// lastTitle finds the last complete title sequence in data. It also returns the start of a
// title sequence left unended at the end of data, for the next read to finish.
func lastTitle(data []byte) (title string, ok bool, partial []byte) {
	for {
		start, prefix := -1, 0
		for _, p := range titleStarts {
			if i := bytes.Index(data, p); i >= 0 && (start < 0 || i < start) {
				start, prefix = i, len(p)
			}
		}
		if start < 0 {
			return title, ok, nil
		}
		body := data[start+prefix:]
		end, term := bytes.IndexByte(body, 0x07), 1
		if st := bytes.Index(body, []byte("\x1b\\")); st >= 0 && (end < 0 || st < end) {
			end, term = st, 2
		}
		if end < 0 {
			if len(body) > maxTitle {
				return title, ok, nil
			}
			return title, ok, append([]byte(nil), data[start:]...)
		}
		if end <= maxTitle {
			title, ok = string(body[:end]), true
		}
		data = body[end+term:]
	}
}

func (t *Terminal) Exited() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// Stream sends the recent output, then output as it comes, until the process exits
// or ctx ends. Each send carries the terminal's size; it is non-zero on the first send
// and whenever the size changed since the last one, and zero otherwise.
func (t *Terminal) Stream(ctx context.Context, send func(data []byte, cols, rows uint16) error) error {
	var off int64
	var sentCols, sentRows uint16
	for {
		t.mu.Lock()
		off = max(off, t.base)
		// In pieces, so a viewer draws the replay as it arrives rather than after all of it.
		rest := t.buf[off-t.base:]
		data := append([]byte(nil), rest[:min(len(rest), maxSend)]...)
		done, changed := t.done, t.changed
		cols, rows := t.cols, t.rows
		t.mu.Unlock()
		resized := cols != sentCols || rows != sentRows
		if len(data) > 0 || resized {
			if !resized {
				cols, rows = 0, 0
			}
			if err := send(data, cols, rows); err != nil {
				return err
			}
			if resized {
				sentCols, sentRows = cols, rows
			}
			off += int64(len(data))
			continue
		}
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil
		}
	}
}

func (t *Terminal) Write(data []byte) error {
	if t.pty == nil {
		return ErrExited
	}
	if _, err := t.pty.Write(data); err != nil {
		return ErrExited
	}
	return nil
}

// Resize sets the terminal's size, in character cells.
func (t *Terminal) Resize(cols, rows uint16) error {
	if t.pty == nil {
		return ErrExited
	}
	if err := t.pty.Resize(cols, rows); err != nil {
		return ErrExited
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols != t.cols || rows != t.rows {
		t.cols, t.rows = cols, rows
		close(t.changed)
		t.changed = make(chan struct{})
	}
	return nil
}

// Close ends the process.
func (t *Terminal) Close() {
	if t.pty != nil {
		t.pty.End()
	}
}
