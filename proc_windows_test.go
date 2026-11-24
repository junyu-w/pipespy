//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestCmdShell(t *testing.T) {
	p, err := NewPipeline(`echo hello "quoted" world| findstr world | findstr /v nothing`, "cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	p.Run(nil)
	waitDone(t, p)
	got := strings.TrimSpace(string(p.Links[3].All()))
	if got != `hello "quoted" world` {
		t.Errorf("output = %q", got)
	}
	if p.ExitCode() != 0 {
		t.Errorf("exit = %d", p.ExitCode())
	}
}

func TestCmdExitCode(t *testing.T) {
	p, _ := NewPipeline(`echo x | exit /b 3`, "cmd.exe")
	p.Run(nil)
	waitDone(t, p)
	if st := p.Stages[1].Info(); st.State != Failed || st.Code != 3 {
		t.Errorf("stage 2 = %+v", st)
	}
}

func TestPowerShell(t *testing.T) {
	p, _ := NewPipeline(`Write-Output 'b','a' | sort`, "powershell")
	p.Run(nil)
	waitDone(t, p)
	if got := strings.Fields(string(p.Links[1].All())); len(got) != 2 || got[0] != "b" {
		t.Errorf("stage 1 output = %q", got)
	}
}

func TestStdinIntoCmd(t *testing.T) {
	p, _ := NewPipeline(`findstr b`, "cmd.exe")
	p.Run(strings.NewReader("a\r\nb\r\nc\r\n"))
	waitDone(t, p)
	if got := strings.TrimSpace(string(p.Links[1].All())); got != "b" {
		t.Errorf("output = %q", got)
	}
}
