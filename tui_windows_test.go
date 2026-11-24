//go:build windows

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/windows"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestTUIInConPTY drives the real pipespy.exe inside a Windows pseudo
// console: it must open CONIN$/CONOUT$, render, accept keystrokes (edit,
// ctrl+u, enter re-runs the pipeline) and quit cleanly on "q".
func TestTUIInConPTY(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "pipespy.exe")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		t.Fatal(err)
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 120, Y: 30}, inR, outW, 0, &hpc); err != nil {
		t.Skipf("ConPTY unavailable: %v", err)
	}
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	screen := &syncBuf{}
	go io.Copy(screen, os.NewFile(uintptr(outR), "conpty-out"))
	keys := os.NewFile(uintptr(inW), "conpty-in")

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		*(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		t.Fatal(err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Don't hand the test runner's redirected stdio to the child; it must
	// talk to the pseudo console only.
	si.Flags = windows.STARTF_USESTDHANDLES

	cmdline := syscall.EscapeArg(exe) + ` --shell cmd "echo hello| findstr hello"`
	argv, _ := windows.UTF16PtrFromString(cmdline)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, argv, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, nil, &si.StartupInfo, &pi); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	defer windows.ClosePseudoConsole(hpc)
	defer windows.TerminateProcess(pi.Process, 1)

	waitFor := func(what string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(ansi.Strip(screen.String()), what) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q; screen output:\n%s", what, ansi.Strip(screen.String()))
	}
	send := func(s string) {
		t.Helper()
		if _, err := keys.WriteString(s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
	}

	waitFor("PIPELINE")
	waitFor("findstr hello")
	waitFor("done")

	// Edit the pipeline in place. %OS% expands only when cmd runs it, so the
	// marker can only appear once the edited pipeline actually re-ran.
	send("i")
	send("\x15") // ctrl+u: clear the input
	send("echo %OS%_marker| findstr marker")
	send("\r")
	waitFor("Windows_NT_marker")
	waitFor("re-ran pipeline")

	send("q")
	if ev, _ := windows.WaitForSingleObject(pi.Process, 15000); ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("pipespy did not exit after q; screen output:\n%s", ansi.Strip(screen.String()))
	}
	var code uint32
	windows.GetExitCodeProcess(pi.Process, &code)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
}
