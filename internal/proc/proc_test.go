package proc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the program under test: a head that starts a child as
// itself, says its id, and waits; the child waits too.
func TestMain(m *testing.M) {
	switch os.Getenv("INABA_PROC_TEST") {
	case "head":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "INABA_PROC_TEST=child")
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			return
		}
		fmt.Println(child.Process.Pid)
		// Ends with its input, unless told to stay.
		if os.Getenv("INABA_PROC_STAY") == "" {
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			return
		}
		time.Sleep(time.Minute)
		return
	case "child":
		time.Sleep(time.Minute)
		return
	}
	os.Exit(m.Run())
}

func gone(pid int) bool {
	for range 100 {
		if !Alive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestGroupEndsWithItsChildren(t *testing.T) {
	for name, stay := range map[string]bool{"asked": false, "killed": true} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "INABA_PROC_TEST=head")
			if stay {
				cmd.Env = append(cmd.Env, "INABA_PROC_STAY=1")
			}
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			g, err := Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(stdout).ReadString('\n')
			child, aerr := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || aerr != nil {
				t.Fatalf("the head said %q: %v", line, err)
			}
			if !Alive(cmd.Process.Pid) || !Alive(child) {
				t.Fatal("the group is not running")
			}
			if stay {
				g.Kill()
			} else {
				g.Ask(stdin)
			}
			_ = cmd.Wait()
			g.Release()
			if !gone(child) {
				t.Fatal("the child outlived its group")
			}
		})
	}
}
