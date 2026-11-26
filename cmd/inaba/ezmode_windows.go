package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// On Windows the host is a Task Scheduler task started when its user signs in. A console
// program would open a window there, so the task runs it in a headless console host,
// and its log goes to a file.

const ezTask = "inaba"

func ezLogPath() string { return filepath.Join(filepath.Dir(dataPath), "inaba.log") }

func ezCheck() error {
	if _, err := exec.LookPath("schtasks"); err != nil {
		return fmt.Errorf("ezmode runs the host as a Task Scheduler task, and schtasks is not here: %w", err)
	}
	return nil
}

func ezInstalled() bool { return exec.Command("schtasks", "/Query", "/TN", ezTask).Run() == nil }

func ezWhat() string { return `the Task Scheduler task \` + ezTask }

func ezLog() string { return ezLogPath() }

func ezStop() error {
	// Ending a task that is not running is not a failure here.
	_ = sysRun("schtasks", "/End", "/TN", ezTask)
	if waitHost(false, 5*time.Second) {
		return nil
	}
	// Ending the task ends the console host it started, which may leave inaba behind.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := fmt.Sprintf(`Get-CimInstance Win32_Process -Filter "Name=%s" | Where-Object { $_.ProcessId -ne %d -and $_.CommandLine -match ' connect( |$)' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`,
		quote(filepath.Base(exe)), os.Getpid())
	return sysRun("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

// ezTaskText is the task running exe with args as account at its sign-in, in dir, logging to log.
func ezTaskText(exe string, args []string, account, dir, log string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	line := []string{"--headless", syscall.EscapeArg(exe)}
	for _, a := range append(args, "--log-file", log) {
		line = append(line, syscall.EscapeArg(a))
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<!-- Written by inaba ezmode install, which writes it afresh; inaba ezmode uninstall removes it. -->
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>inaba host: runs coding agents on this machine for an inaba-server.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%[1]s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%[1]s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <StartWhenAvailable>true</StartWhenAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%[2]s</Command>
      <Arguments>%[3]s</Arguments>
      <WorkingDirectory>%[4]s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, esc(account), esc(conhost), esc(strings.Join(line, " ")), esc(dir))
}

func ezStart(exe string, args []string) ([]string, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	task := ezTaskText(exe, args, u.Username, filepath.Dir(dataPath), ezLogPath())
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		return nil, err
	}
	// schtasks reads the definition as UTF-16, as its declaration says.
	f, err := os.CreateTemp("", "inaba-task-*.xml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	body := append([]uint16{0xFEFF}, utf16.Encode([]rune(task))...)
	if err := binary.Write(f, binary.LittleEndian, body); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := sysRun("schtasks", "/Create", "/TN", ezTask, "/XML", f.Name(), "/F"); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "access is denied") {
			return nil, fmt.Errorf("%w; this Windows lets only administrators add a task that starts at sign-in: run this once from a terminal opened as administrator (the host still runs as %s)", err, u.Username)
		}
		return nil, err
	}
	if err := sysRun("schtasks", "/Run", "/TN", ezTask); err != nil {
		return nil, err
	}
	return []string{"It starts whenever you sign in to Windows as " + u.Username + ", and runs while you are signed in."}, nil
}

func ezRemove() error { return sysRun("schtasks", "/Delete", "/TN", ezTask, "/F") }
