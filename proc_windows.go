//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// defaultShell prefers a POSIX shell (Git for Windows, MSYS2, Cygwin) so
// Unix-style pipelines work, and falls back to cmd.exe.
func defaultShell() string {
	for _, s := range []string{"sh", "bash"} {
		if p, err := exec.LookPath(s); err == nil {
			return p
		}
	}
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return "cmd.exe"
}

func shellKind(shell string) string {
	base := strings.ToLower(filepath.Base(shell))
	return strings.TrimSuffix(base, ".exe")
}

func shellCommand(shell, script string) *exec.Cmd {
	var cmd *exec.Cmd
	switch shellKind(shell) {
	case "cmd":
		// cmd.exe does its own command-line parsing; pass the script verbatim.
		cmd = exec.Command(shell)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CmdLine: syscall.EscapeArg(shell) + ` /d /s /c "` + script + `"`,
		}
	case "powershell", "pwsh":
		cmd = exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	default:
		cmd = exec.Command(shell, "-c", script)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Keep console Ctrl+C events away from stages; pipespy manages them.
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	return cmd
}

func killTree(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

func exitState(err error, killed bool) (StageState, int, string) {
	if err == nil {
		return Exited, 0, ""
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Failed, 1, err.Error()
	}
	code := ee.ExitCode()
	switch {
	case killed:
		return Killed, code, ""
	case code == 141: // MSYS/Cygwin programs report SIGPIPE this way
		return PipeClosed, code, ""
	case code == 0:
		return Exited, 0, ""
	}
	return Failed, code, ""
}

func openTTY() (in, out *os.File, err error) {
	in, err = os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	out, err = os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		in.Close()
		return nil, nil, err
	}
	return in, out, nil
}

// prepareConsole turns on ANSI escape processing for the console.
func prepareConsole(out *os.File) (restore func()) {
	h := windows.Handle(out.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}
	}
	_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.ENABLE_PROCESSED_OUTPUT)
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
