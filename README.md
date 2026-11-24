# pipespy

A terminal UI that shows the data flowing through every pipe of a shell pipeline.

![pipespy demo: stepping through a six-stage log pipeline, then editing it in place](docs/demo.gif)

```sh
cat access.log | pipespy 'grep -v kube-probe | awk "{print \$9, \$7}" | grep "^[45]" | sort | uniq -c | sort -rn'
```

<sub>The demo is generated from [`docs/demo/demo.tape`](docs/demo/demo.tape) with
[VHS](https://github.com/charmbracelet/vhs); re-record with
`go build -o pipespy . && vhs docs/demo/demo.tape`.</sub>

pipespy runs each stage in its own shell and sits on every pipe in between,
recording what passes through. You can select any stage and see:

- its **input** and **output** side by side, with line numbers
- **diff marks**: input lines the stage dropped (red), output lines it created (green)
- **stderr**, exit status (`exit 0`, `exit 2`, `SIGPIPE`), and wall time
- a **throughput sparkline** for every pipe on a shared time axis, so you can
  see which stage buffers and which one streams
- an automatic **hex view** for binary data (`gzip`, images, …)

## Install

**macOS / Linux**

```sh
brew install junyu-w/tap/pipespy
```

**Windows**: download `pipespy_<version>_windows_amd64.zip` (or `arm64`) from
[Releases](https://github.com/junyu-w/pipespy/releases), unzip, and put
`pipespy.exe` on your PATH. Use Windows Terminal or the regular console
(mintty, the default Git Bash window, isn't a Windows console).

Stages run in `sh`/`bash` if one is on PATH (Git for Windows, MSYS2), so
Unix-style pipelines work; otherwise in `cmd.exe`. Pick explicitly with
`--shell cmd`, `--shell powershell` or `--shell pwsh`.

**From source**: `go build -o pipespy .`

## Usage

```sh
pipespy 'cmd1 | cmd2 | cmd3'             # first stage reads /dev/null
producer | pipespy 'cmd1 | cmd2'         # piped stdin feeds the first stage
pipespy 'cmd1 | cmd2' > result.txt       # final output is written on quit
pipespy ls -la '|' wc -l                 # unquoted form, with literal '|'
```

`--shell bash` (or `PIPESPY_SHELL`) chooses the shell that runs each stage;
the default is `/bin/sh` (see above for Windows). The exit status is the last
stage's, as in a shell.

`--report` runs without the TUI, for scripts and CI: the final output goes to
stdout and a per-stage table (status, time, lines/bytes in and out, stderr)
to stderr.

## Keys

| key | action |
| --- | --- |
| `↑` `↓` / `j` `k` | select stage (sidebar) or scroll (pane) |
| `[` `]`, `1`–`9` | previous / next / jump to stage |
| `tab` / `shift+tab` | move focus: pipeline → input → output → stderr |
| `←` `→` | pan horizontally |
| `pgup` `pgdn`, `g` `G` | page, top, end (`G` keeps following live data) |
| `i` | edit the pipeline in place, `⏎` to re-run it on the same stdin |
| `r` | re-run |
| `d` `s` `x` `e` | toggle diff marks, synced scrolling, hex, stderr pane |
| mouse | wheel scrolls, click selects stages and panes |
| `q` | quit |

## Notes

- Each stream keeps up to 64 MB for display; bytes past that are still counted
  and passed along (marked ✂).
- Many tools block-buffer when writing to a pipe, so output can arrive in one
  burst at the end. pipespy shows that faithfully; use `grep --line-buffered`
  or similar to stream.
- `|&` isn't supported; write `2>&1 |` instead.

## Releasing

Push a tag: `git tag v0.2.0 && git push origin v0.2.0`. The
[release workflow](.github/workflows/release.yml) then:

1. runs the tests on Linux, macOS and Windows (including a ConPTY test that
   drives the real TUI on Windows),
2. cross-compiles macOS, Linux and Windows binaries for amd64 and arm64,
3. smoke-tests each archive on its own OS and architecture,
4. publishes a GitHub Release with archives and `checksums.txt`,
5. updates `Formula/pipespy.rb` in
   [junyu-w/homebrew-tap](https://github.com/junyu-w/homebrew-tap), and
6. installs it with `brew` on a fresh macOS runner to confirm.

Run the workflow manually (Actions → release → Run workflow) for a dry run
of steps 1–3. Step 5 needs a `TAP_DEPLOY_KEY` secret: the private half of a
deploy key with write access to the tap repo. `scripts/setup-tap-key.sh`
sets both up (one time).
