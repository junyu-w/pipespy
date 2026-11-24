# pipespy

A terminal UI that shows the data flowing through every pipe of a shell pipeline.

```
pipespy 'cat access.log | grep " 404 " | awk "{print \$7}" | sort | uniq -c | sort -rn'
```

pipespy runs each stage in its own shell and sits on every pipe in between,
recording what passes through. You can select any stage and see:

- its **input** and **output** side by side, with line numbers
- **diff marks**: input lines the stage dropped (red), output lines it created (green)
- **stderr**, exit status (`exit 0`, `exit 2`, `SIGPIPE`), and wall time
- a **throughput sparkline** for every pipe on a shared time axis, so you can
  see which stage buffers and which one streams
- an automatic **hex view** for binary data (`gzip`, images, …)

## Install

```sh
brew install junyu-w/tap/pipespy
```

or from source: `go build -o pipespy . && mv pipespy /usr/local/bin/`

### Releasing (maintainers)

1. Push this repo to `github.com/<user>/pipespy` and tag a release:
   `git tag v0.1.0 && git push origin v0.1.0`
2. Render the formula against the tag tarball:
   `scripts/render-formula.sh <user> 0.1.0 > pipespy.rb`
3. Commit `pipespy.rb` to `Formula/` in `github.com/<user>/homebrew-tap`.

`brew install --HEAD <user>/tap/pipespy` builds from `main`.

## Usage

```sh
pipespy 'cmd1 | cmd2 | cmd3'             # first stage reads /dev/null
producer | pipespy 'cmd1 | cmd2'         # piped stdin feeds the first stage
pipespy 'cmd1 | cmd2' > result.txt       # final output is written on quit
pipespy ls -la '|' wc -l                 # unquoted form, with literal '|'
```

`--shell bash` (or `PIPESPY_SHELL`) chooses the shell that runs each stage;
the default is `/bin/sh`. The exit status is the last stage's, as in a shell.

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
