//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func defaultShell() string { return "/bin/sh" }

// shellCommand runs script in its own process group so the whole stage
// (including anything it spawns) can be killed at once.
func shellCommand(shell, script string) *exec.Cmd {
	cmd := exec.Command(shell, "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func killTree(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

func exitState(err error, killed bool) (StageState, int, string) {
	if err == nil {
		return Exited, 0, ""
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Failed, 1, err.Error()
	}
	ws, _ := ee.Sys().(syscall.WaitStatus)
	switch {
	case ws.Signaled() && ws.Signal() == syscall.SIGPIPE:
		return PipeClosed, 141, ""
	case ws.Signaled() && killed:
		return Killed, 128 + int(ws.Signal()), ""
	case ws.Signaled():
		return Failed, 128 + int(ws.Signal()), ws.Signal().String()
	case ws.ExitStatus() == 141:
		return PipeClosed, 141, ""
	}
	return Failed, ws.ExitStatus(), ""
}

// openTTY returns the controlling terminal, independent of stdin/stdout,
// which may both be pipes.
func openTTY() (in, out *os.File, err error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	return f, f, err
}

func prepareConsole(*os.File) (restore func()) { return func() {} }
