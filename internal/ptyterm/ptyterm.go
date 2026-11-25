// Package ptyterm runs a command on a pseudo-terminal and keeps its recent output, for
// any number of viewers to attach to, as they come and go.
package ptyterm

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// How much output a late viewer still gets to see.
const scrollback = 256 << 10

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
}

// Start runs argv in dir on a new PTY.
// Start runs argv in dir on a terminal of cols by rows; zero for either means 80 by 24.
func Start(argv []string, dir string, cols, rows uint16) (*Terminal, error) {
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	p, err := startPTY(argv, dir, append(os.Environ(), "TERM=xterm-256color"), cols, rows)
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
		data := append([]byte(nil), t.buf[off-t.base:]...)
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
