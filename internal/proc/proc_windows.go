//go:build windows

package proc

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Group is a started program and the processes it started, held together by a job object.
type Group struct {
	mu  sync.Mutex
	job windows.Handle
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// NewJob makes a job object that ends every process in it once its last handle closes,
// which Windows also does when this process goes.
func NewJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	runtime.KeepAlive(&limits)
	return job, nil
}

// Start starts cmd in a job of its own. It starts suspended, so nothing it starts in turn
// can come before it is in the job.
func Start(cmd *exec.Cmd) (*Group, error) {
	job, err := NewJob()
	if err != nil {
		return nil, fmt.Errorf("making a job: %w", err)
	}
	// In a process group of its own, Ctrl+C in this console leaves it alone.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if err := adopt(job, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	return &Group{job: job}, nil
}

func adopt(job windows.Handle, pid int) error {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(job, p); err != nil {
		return fmt.Errorf("putting the process in a job: %w", err)
	}
	if status, _, _ := ntResumeProcess.Call(uintptr(p)); status != 0 {
		return fmt.Errorf("resuming the process: %w", windows.NTStatus(status))
	}
	return nil
}

// Ask asks the group to end. Windows has no SIGTERM: a program that reads its input, as an
// agent does, ends with it.
func (g *Group) Ask(stdin io.Closer) { _ = stdin.Close() }

// Kill ends the group at once.
func (g *Group) Kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
	}
}

// Release lets go of the group once its head has exited, ending what is left of it.
func (g *Group) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		windows.CloseHandle(g.job)
		g.job = 0
	}
}

// Alive reports whether a process with that id is running.
func Alive(pid int) bool {
	p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(p)
	// Its handle is signaled once it has exited.
	event, err := windows.WaitForSingleObject(p, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}
