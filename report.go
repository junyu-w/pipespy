package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// runReport waits for the pipeline, writes the final output to out and a
// per-stage table to summary. It returns the pipeline's exit code.
func runReport(pl *Pipeline, out, summary io.Writer) int {
	for !pl.Done() {
		time.Sleep(5 * time.Millisecond)
	}
	last := pl.Links[len(pl.Links)-1]
	out.Write(last.All())

	fmt.Fprintf(summary, "pipespy: %d %s in %s\n", len(pl.Stages),
		plural(int64(len(pl.Stages)), "stage", "stages"), humanDur(pl.End().Sub(pl.Start)))
	tw := tabwriter.NewWriter(summary, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tstatus\ttime\tin\tout\tstderr\tcommand")
	for i, s := range pl.Stages {
		info := s.Info()
		in, o := pl.Links[i].Stats(), pl.Links[i+1].Stats()
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%d\t%s\n", i+1, statusText(info),
			humanDur(info.Duration()), flow(in), flow(o), s.Stderr.Stats().Lines, oneLine(s.Cmd))
	}
	tw.Flush()
	for i, s := range pl.Stages {
		if e := strings.TrimRight(string(s.Stderr.All()), "\n"); e != "" {
			fmt.Fprintf(summary, "  stderr of stage %d:\n    %s\n", i+1, strings.ReplaceAll(e, "\n", "\n    "))
		}
	}
	if last.Stats().Truncated {
		fmt.Fprintf(summary, "pipespy: final output exceeded %s and was truncated\n", humanBytes(maxCapture))
	}
	return pl.ExitCode()
}

func flow(s CapStats) string { return humanCount(s.Lines) + " ln/" + humanBytes(s.Bytes) }

func statusText(i StageInfo) string {
	switch i.State {
	case Exited:
		return "exit 0"
	case PipeClosed:
		return "SIGPIPE"
	case Killed:
		return "killed"
	case Failed:
		if i.Err != "" {
			return i.Err
		}
		return fmt.Sprintf("exit %d", i.Code)
	}
	return "running"
}
