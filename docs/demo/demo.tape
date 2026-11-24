# Renders docs/demo.gif. From the repo root:
#   go build -o pipespy . && vhs docs/demo/demo.tape
Output docs/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1440
Set Height 800
Set Padding 18
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 28ms
Set PlaybackSpeed 1.0

Hide
Type `export PATH="$PWD:$PATH" PS1='\[\e[35m\]❯\[\e[0m\] ' && cd docs/demo && clear`
Enter
Sleep 500ms
Show

# Which 4xx/5xx errors happen most, ignoring health checks?
Type `cat access.log | pipespy 'grep -v kube-probe | awk "{print \$9, \$7}" | grep "^[45]" | sort | uniq -c | sort -rn'`
Sleep 700ms
Enter
Sleep 3s

# Walk the pipeline: each stage's input and output, with dropped (red) and new (green) lines
Type "]"
Sleep 2.2s
Type "]"
Sleep 2.2s
Down@120ms 6
Sleep 1s
Type "]"
Sleep 1.6s
Type "]"
Sleep 1.6s
Type "]"
Sleep 2.5s

# Edit the pipeline in place and re-run it on the same stdin
Type "i"
Sleep 600ms
Type " | head -3"
Sleep 600ms
Enter
Sleep 1.2s
Type "7"
Sleep 2.8s

# Back to the first stage, then quit
Type "1"
Sleep 1.5s
Type "q"
Sleep 800ms
