//go:build windows

package ptyterm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Alex4386/inaba/internal/proc"
)

// conPTY is a Windows pseudo console (Windows 10 1809 and later) with a process on it.
type conPTY struct {
	in, out *os.File
	process windows.Handle
	// exited closes once the process has exited and its console is gone.
	exited chan struct{}

	mu           sync.Mutex
	console, job windows.Handle
}

func startPTY(argv []string, dir string, env []string, cols, rows uint16) (_ pseudo, err error) {
	path, err := exec.LookPath(argv[0])
	if err == nil {
		// A relative path would be read against dir rather than where it was found.
		path, err = filepath.Abs(path)
	}
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, argv[1:]...)))
	if err != nil {
		return nil, err
	}
	folder, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}

	// What was opened before a failure is closed again.
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	closing := func(h windows.Handle) { undo = append(undo, func() { windows.CloseHandle(h) }) }

	var inR, inW, outR, outW windows.Handle
	if err = windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	// The console keeps copies of the ends it uses.
	defer windows.CloseHandle(inR)
	closing(inW)
	if err = windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(outW)
	closing(outR)

	var console windows.Handle
	if err = windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &console); err != nil {
		return nil, err
	}
	undo = append(undo, func() { windows.ClosePseudoConsole(console) })
	job, err := proc.NewJob()
	if err != nil {
		return nil, err
	}
	closing(job)

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the console's handle itself, not a pointer to it.
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&console)), unsafe.Sizeof(console)); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// With no handles named, the process takes the console's rather than this process's own.
	si.Flags = windows.STARTF_USESTDHANDLES
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err = windows.CreateProcess(nil, line, nil, nil, false, flags, &envBlock(env)[0], folder, &si.StartupInfo, &pi); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(pi.Thread)
	closing(pi.Process)
	// Suspended until it is in the job, so nothing it starts can come before that.
	if err = windows.AssignProcessToJobObject(job, pi.Process); err == nil {
		_, err = windows.ResumeThread(pi.Thread)
	}
	if err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		return nil, err
	}

	c := &conPTY{
		in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out"),
		process: pi.Process, console: console, job: job, exited: make(chan struct{}),
	}
	go c.follow()
	return c, nil
}

// follow takes the console away once the process exits, which ends its output: the console
// would otherwise hold it open.
func (c *conPTY) follow() {
	_, _ = windows.WaitForSingleObject(c.process, windows.INFINITE)
	c.mu.Lock()
	console := c.console
	c.console = 0
	c.mu.Unlock()
	// Not under the lock: this waits for the last output to be read.
	windows.ClosePseudoConsole(console)
	close(c.exited)
}

func (c *conPTY) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *conPTY) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *conPTY) Resize(cols, rows uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.console == 0 {
		return errors.New("the console is closed")
	}
	return windows.ResizePseudoConsole(c.console, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// End ends the process and all it started; follow then ends the output.
func (c *conPTY) End() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job != 0 {
		_ = windows.TerminateJobObject(c.job, 1)
	}
}

func (c *conPTY) Wait() {
	<-c.exited
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.in.Close()
	_ = c.out.Close()
	windows.CloseHandle(c.process)
	windows.CloseHandle(c.job)
	c.job = 0
}

// envBlock is env as CreateProcess takes it: each entry ended by a NUL, and one more at the
// end. Of entries with one name, the last stays, as os/exec has it.
func envBlock(env []string) []uint16 {
	last := map[string]int{}
	name := func(kv string) string {
		// Windows keeps entries such as "=C:=C:\dir", whose names start with "=".
		if i := strings.Index(kv[min(1, len(kv)):], "="); i >= 0 {
			return strings.ToUpper(kv[:i+1])
		}
		return strings.ToUpper(kv)
	}
	for i, kv := range env {
		last[name(kv)] = i
	}
	var block []uint16
	for i, kv := range env {
		if kv == "" || last[name(kv)] != i || strings.ContainsRune(kv, 0) {
			continue
		}
		block = append(append(block, utf16.Encode([]rune(kv))...), 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0)
}
