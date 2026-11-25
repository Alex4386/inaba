package ptyterm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the program on the terminal: it says where it runs and
// answers each line, until told to leave.
func TestMain(m *testing.M) {
	if os.Getenv("INABA_PTY_TEST") == "1" {
		dir, _ := os.Getwd()
		fmt.Printf("ready in %s as %s\n", dir, os.Getenv("TERM"))
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			if strings.TrimSpace(in.Text()) == "leave" {
				return
			}
			fmt.Println("heard " + in.Text())
		}
		return
	}
	os.Exit(m.Run())
}

func start(t *testing.T, dir string) (*Terminal, context.Context) {
	t.Helper()
	t.Setenv("INABA_PTY_TEST", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	term, err := Start([]string{exe}, dir, 90, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return term, ctx
}

func TestStreamSendsSize(t *testing.T) {
	term, ctx := start(t, t.TempDir())
	sizes := make(chan [2]uint16, 4)
	go term.Stream(ctx, func(_ []byte, cols, rows uint16) error {
		if cols != 0 {
			sizes <- [2]uint16{cols, rows}
		}
		return nil
	})
	want := func(size [2]uint16) {
		t.Helper()
		select {
		case got := <-sizes:
			if got != size {
				t.Fatalf("size %v, want %v", got, size)
			}
		case <-ctx.Done():
			t.Fatalf("no size %v sent", size)
		}
	}
	want([2]uint16{90, 30})
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	want([2]uint16{120, 40})
}

// What is typed reaches the process, what it prints reaches the viewer, and its exit
// ends the stream and the terminal.
func TestTerminalRunsAProcess(t *testing.T) {
	term, ctx := start(t, t.TempDir())
	out := make(chan []byte, 64)
	done := make(chan error, 1)
	go func() {
		done <- term.Stream(ctx, func(data []byte, _, _ uint16) error {
			out <- data
			return nil
		})
	}()
	var got bytes.Buffer
	waitFor := func(text string) {
		t.Helper()
		for !strings.Contains(got.String(), text) {
			select {
			case b := <-out:
				got.Write(b)
			case <-ctx.Done():
				t.Fatalf("no %q in %q", text, got.String())
			}
		}
	}
	waitFor("ready in ")
	waitFor("xterm-256color")
	if err := term.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitFor("heard hello")
	if err := term.Write([]byte("leave\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil || !term.Exited() {
			t.Fatalf("stream ended with %v, context %v, exited %v", err, ctx.Err(), term.Exited())
		}
	case <-ctx.Done():
		t.Fatal("the stream outlived the process")
	}
}

func TestCloseEndsTheProcess(t *testing.T) {
	term, ctx := start(t, t.TempDir())
	term.Close()
	for !term.Exited() {
		select {
		case <-ctx.Done():
			t.Fatal("the process outlived Close")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if term.Resize(80, 24) == nil {
		t.Fatal("resized a closed terminal")
	}
}

func TestStartSaysWhatIsMissing(t *testing.T) {
	if _, err := Start([]string{"inaba-no-such-program"}, t.TempDir(), 0, 0); err == nil {
		t.Fatal("started a program that does not exist")
	}
}
