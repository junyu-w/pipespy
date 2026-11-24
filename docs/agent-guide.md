# pipespy for AI agents

pipespy runs a shell pipeline and records the data on **every pipe**: each
stage's input, output and stderr, plus exit status and timing. Use it when a
pipeline gives the wrong result and you need to see *which stage* is at
fault, instead of re-running growing prefixes of the pipeline by hand.

Run the pipeline once with `pipespy run`, read the summary, then query the
saved run with `inspect`, `diff` and `trace`; none of these re-run anything.
Output is plain text by default and bounded so it fits in your context; add
`--json` to any command for a stable, machine-readable document.

## Quick start

```sh
pipespy run 'grep -v DEBUG app.log | cut -d" " -f3 | sort | uniq -c | sort -rn | head'
```

The summary shows one row per stage (status, exit code, time, lines/bytes in
and out, stderr), the start of the final output, **hints** that point at the
likely problem, and the exact follow-up commands to run next. Example:

```
run 20261124-091502-3f2a · failed · exit 1 · 4 stages · 16ms
pipeline: grep -v kube-probe access.log | awk "{print \$9, \$7}" | grep "^5" | grep "api/user"
stdin:    none

  #  status  exit  time  in                  out                 stderr  command
  1  ok      0     11ms  0 ln · 0 B          1325 ln · 125.7 KB  -       grep -v kube-probe access.log
  2  ok      0     12ms  1325 ln · 125.7 KB  1325 ln · 18.7 KB   -       awk "{print \$9, \$7}"
  3  ok      0     11ms  1325 ln · 18.7 KB   95 ln · 1.5 KB      -       grep "^5"
  4  failed  1     10ms  95 ln · 1.5 KB      0 ln · 0 B          -       grep "api/user"

hints:
  - stage 4 (grep "api/user") failed with exit code 1; for grep, exit 1 with no stderr means "no match" (or "false"), not a crash
  - data disappears at stage 4 (grep "api/user"): 95 input lines, no output. Look at what it received: pipespy inspect 20261124-091502-3f2a --stage 4 --stream in --head 20
```

## Commands

Every command except `run` takes an optional run id as its first argument;
without it, the most recent run is used. Ids can be shortened to any unique
prefix or suffix (e.g. the last 4 characters).

### `pipespy run [flags] '<pipeline>'`

Runs the pipeline, saves every stream, prints the summary.

| flag | default | meaning |
| --- | --- | --- |
| `--json` | off | print the summary as JSON |
| `--timeout D` | `60s` | kill the pipeline after D (`0` = no limit); exit code becomes 124 |
| `--sample N` | `5` | lines of each stage's output in the JSON summary |
| `--shell S` | `/bin/sh` | shell for each stage (`bash`, `zsh`, `cmd`, `powershell`, `pwsh`) |
| `--no-stdin` | off | ignore pipespy's own stdin; stage 1 gets empty input |
| `--no-save` | off | don't save the run (inspect/diff/trace won't work for it) |

Data piped into pipespy feeds stage 1: `cat data.csv | pipespy run 'cut -d, -f2 | sort -u'`.
If your environment leaves stdin open without sending anything, a stage that
reads stdin waits until `--timeout`; pass `--no-stdin` or give the file as an
argument instead.

Exit code: the **last stage's** exit code, like a shell (`124` on timeout,
`2` for a malformed pipeline). The `status` field is `failed` if **any**
stage failed, even when the last one succeeded — check both.

### `pipespy inspect [RUN] [selection]`

Prints one recorded stream with line numbers (`N<TAB>text`).

| flag | meaning |
| --- | --- |
| `--stage N` | stage number, 1-based (default: last stage) |
| `--stream out\|in\|err` | the stage's output (default), input, or stderr |
| `--final` / `--stdin` | shortcuts for the pipeline's final output / its stdin |
| `--head N`, `--tail N` | first / last N selected lines |
| `--range A:B` | lines A to B, inclusive (`A:` and `:B` also work) |
| `--grep RE` | only lines matching a Go (RE2) regular expression; add `-i` to ignore case |
| `--all` | no limit (without it, at most 50 lines are shown) |
| `--max-width N` | clip long lines (default 400 characters; `0` = never) |
| `--hex` | hex dump (automatic for binary data) |
| `--json` | JSON output |

The input of stage N is the output of stage N-1, so `--stage 3 --stream in`
and `--stage 2` show the same data. When output is cut off, the last line
tells you how to continue (for example `--range 51: --head 50`).

### `pipespy diff [RUN] --stage N`

What stage N removed from its input and added to its output, with counts and
a sample of each (`--limit N`, default 10). Line-based and position-free: a
modified line shows up as one removed plus one added; `order changed` means
the stage reordered lines it kept (like `sort`). Use it to check what a
filter (`grep`, `awk`, `sed`, `uniq`) actually did.

### `pipespy trace [RUN] --match RE`

Counts the lines matching RE in every pipe and names the stage where they are
dropped or lost. Use it to answer "why is X missing from the result?".

```
  pipe                                    lines  matches  first match
  stdin                                   0      0        -
  after stage 1: grep -v kube-probe ...   1325   162      1: 10.0.6.168 ... "GET /api/users ...
  after stage 2: awk "{print \$9, \$7}"   1325   162      1: 200 /api/users
  after stage 3: grep "^5"                95     0        -  ← lost here

matching lines are lost at stage 3 (grep "^5"): 162 in, 0 out. ...
```

### `pipespy runs` and `pipespy guide`

`runs` lists saved runs, newest first (`--json`, `--limit N`). `guide` prints
this document.

## Recipes

**Wrong or empty result.** `pipespy run '<pipeline>'`, then read the hints.
The `in`/`out` columns show where line counts change unexpectedly; inspect
that stage's input and output:
`pipespy inspect --stage 3 --stream in --head 20` and `pipespy inspect --stage 3 --head 20`.

**A value is missing from the output.** `pipespy trace --match 'the value'`
shows the stage that drops it; then `pipespy inspect --stage N --stream in --grep 'the value'`
shows the lines that stage received.

**A filter removes too much or too little.** `pipespy diff --stage N`.

**A stage failed.** `pipespy inspect --stage N --stream err`. Exit 127 means
command not found; exit 1 from grep-like tools means no match.

**Huge output.** Never print everything: use `--grep`, `--tail`, `--range`,
or `--json` with `total_lines` to decide what to read.

**Fix and compare.** Edit the pipeline and `run` it again; each run gets a
new id and old runs stay available (`pipespy runs`), so you can inspect the
same stage in the old and new run.

## JSON shapes

`run --json`:

```json
{
  "id": "20261124-091502-3f2a", "saved": true,
  "pipeline": "...", "shell": "/bin/sh",
  "status": "ok | failed | timeout", "exit_code": 0, "duration_ms": 12.5,
  "stdin": {"bytes": 0, "lines": 0} | null,
  "output": {"bytes": 14, "lines": 2},
  "output_head": ["first lines of the final output"],
  "stages": [{
    "stage": 1, "command": "sort",
    "status": "ok | failed | sigpipe | killed | timeout", "exit_code": 0, "error": "",
    "duration_ms": 9.1,
    "input":  {"bytes": 6, "lines": 3},
    "output": {"bytes": 6, "lines": 3, "truncated": false, "binary": false},
    "stderr": {"bytes": 0, "lines": 0},
    "output_head": ["..."], "output_tail": ["..."], "stderr_text": "..."
  }],
  "hints": ["..."],
  "next": ["pipespy inspect ... "]
}
```

`inspect --json`: `{run, stage, stream, command, total_lines, total_bytes,
truncated, format ("text"|"hex"), grep, matches, lines: [{n, text}],
more_before, more_after}`.

`diff --json`: `{run, stage, command, input_lines, output_lines, kept,
removed, added, reordered, removed_sample: [{n, text}], added_sample: [{n, text}]}`.

`trace --json`: `{run, pattern, pipes: [{pipe, label, lines, matches,
first_match_line, first_match}], first_drop_stage, lost_at_stage, verdict}`.
`pipe` 0 is stdin; pipe k is the output of stage k.

Errors with `--json` are `{"error": "..."}` with a non-zero exit code.

## Things to know

- Each stage runs in its own shell process (`sh -c`), exactly as in a shell
  pipeline. Variables, `cd`, and functions don't carry between stages.
- `stage | other` splitting respects quotes, `$(...)`, backticks and
  subshells. `||` is not a pipe. `|&` isn't supported; use `2>&1 |`.
- `sigpipe` is normal: a stage stopped early because the next one (e.g.
  `head`) stopped reading.
- Lines are shown with terminal escape codes removed and other control
  characters written as `\xNN`. Binary streams are shown as hex dumps.
- Each stream keeps up to 64 MB; beyond that it is marked `truncated`.
- Runs are stored under the user cache directory (`~/Library/Caches/pipespy`
  on macOS, `~/.cache/pipespy` on Linux, `%LocalAppData%\pipespy` on
  Windows); set `PIPESPY_HOME` to change it. The 50 most recent runs are kept.
- Stages run for real: a pipeline with side effects (writes, network calls)
  has them every time you `run` it. Inspecting a saved run never re-runs it.
- On Windows, stages use `sh` when it is on PATH (Git for Windows), else
  `cmd.exe`; pick explicitly with `--shell`.

## Snippet for AGENTS.md / CLAUDE.md

```markdown
When a shell pipeline produces unexpected output, debug it with pipespy
instead of re-running partial pipelines: `pipespy run '<pipeline>'` records
every stage's input/output/stderr and prints hints; then use
`pipespy inspect --stage N [--stream in|err] [--grep RE]`,
`pipespy diff --stage N` and `pipespy trace --match RE` on the saved run.
`pipespy guide` has the details.
```
