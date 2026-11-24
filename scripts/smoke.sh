#!/usr/bin/env bash
# End-to-end checks against a built pipespy binary (no TTY needed).
#   scripts/smoke.sh ./pipespy [expected-version]
set -euo pipefail
bin=$1
want_version=${2:-}
fail() { echo "FAIL: $*" >&2; exit 1; }
norm() { tr -d '\r'; }

v=$("$bin" --version | norm)
echo "version: $v"
[ -z "$want_version" ] || [ "$v" = "pipespy $want_version" ] || fail "version is '$v', want '$want_version'"

out=$(printf 'b\na\nb\n' | "$bin" --report 'sort | uniq -c | sort -rn' 2>report.txt | norm)
[ "$(echo "$out" | head -1 | tr -s ' ' | sed 's/^ //')" = "2 b" ] || fail "uniq pipeline: $out"
grep -q "3 stages" report.txt || fail "report missing: $(cat report.txt)"

out=$("$bin" --report 'yes | head -3' 2>/dev/null | norm)
[ "$out" = $'y\ny\ny' ] || fail "yes|head: $out"

set +e
"$bin" --report 'echo x | sh -c "cat >/dev/null; exit 4"' >/dev/null 2>&1
code=$?
set -e
[ "$code" = 4 ] || fail "exit code $code, want 4"

"$bin" 'ls |' >/dev/null 2>&1 && fail "bad pipeline accepted" || true

# agent mode: run once, then query the saved run
export PIPESPY_HOME
PIPESPY_HOME=$(mktemp -d)
printf 'a\nb\n' | "$bin" run --json 'sort -r | head -1' > run.json || fail "agent run exit $?"
grep -q '"status": "ok"' run.json || fail "agent run: $(cat run.json)"
"$bin" inspect --stage 1 | norm | grep -q $'^ *1\tb$' || fail "inspect: $("$bin" inspect --stage 1)"
"$bin" trace --match a | norm | grep -q "lost at stage 2" || fail "trace: $("$bin" trace --match a)"
"$bin" diff --stage 2 --json | norm | grep -q '"removed": 1' || fail "diff: $("$bin" diff --stage 2)"
"$bin" runs | norm | grep -q "sort -r" || fail "runs"
"$bin" guide | norm | grep -q "pipespy for AI agents" || fail "guide"
set +e
"$bin" run --timeout 300ms --no-stdin 'sleep 5' >/dev/null
code=$?
set -e
[ "$code" = 124 ] || fail "timeout exit $code, want 124"
rm -rf "$PIPESPY_HOME" run.json

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*)
  out=$("$bin" --report --shell cmd 'echo hello world| findstr world' 2>/dev/null | norm | sed 's/ *$//')
  [ "$out" = "hello world" ] || fail "cmd shell: '$out'"
  out=$("$bin" --report --shell powershell "Write-Output 'ps-ok'" 2>/dev/null | norm)
  [ "$out" = "ps-ok" ] || fail "powershell: '$out'"
  PIPESPY_HOME=$(mktemp -d) "$bin" run --no-stdin --shell cmd 'echo hello| findstr hello' | norm | grep -q "hello" || fail "agent run with cmd"
  ;;
esac
rm -f report.txt
echo "smoke OK"
