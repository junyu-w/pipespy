#!/usr/bin/env bash
# Print release notes for a tag: commits since the previous tag, grouped by
# conventional prefix when present, followed by install instructions.
#   scripts/release-notes.sh v0.2.0 junyu-w
set -euo pipefail
tag=$1
owner=$2
version=${tag#v}

prev=$(git describe --tags --abbrev=0 --match 'v*' "$tag^" 2>/dev/null || true)
range=${prev:+$prev..}$tag

feat=() fix=() other=()
while IFS=$'\t' read -r hash subject; do
  case "$subject" in
  Merge\ *) continue ;;
  feat:* | feat\(*) feat+=("- ${subject#*: } ($hash)") ;;
  fix:* | fix\(*) fix+=("- ${subject#*: } ($hash)") ;;
  chore:* | ci:* | docs:* | test:* | build:*) other+=("- $subject ($hash)") ;;
  *) other+=("- $subject ($hash)") ;;
  esac
done < <(git log --no-merges --reverse --format=$'%h\t%s' "$range")

echo "## Changes${prev:+ since $prev}"
echo
if [ ${#feat[@]} -gt 0 ]; then printf '### Features\n\n'; printf '%s\n' "${feat[@]}"; echo; fi
if [ ${#fix[@]} -gt 0 ]; then printf '### Fixes\n\n'; printf '%s\n' "${fix[@]}"; echo; fi
if [ ${#other[@]} -gt 0 ]; then
  [ ${#feat[@]} -gt 0 ] || [ ${#fix[@]} -gt 0 ] && printf '### Other\n\n'
  printf '%s\n' "${other[@]}"
  echo
fi
[ ${#feat[@]} -gt 0 ] || [ ${#fix[@]} -gt 0 ] || [ ${#other[@]} -gt 0 ] || { echo "_No changes._"; echo; }

cat <<EOF
## Install

**macOS / Linux (Homebrew)**

\`\`\`sh
brew install $owner/tap/pipespy
\`\`\`

**Windows (Scoop)**

\`\`\`powershell
scoop bucket add $owner https://github.com/$owner/scoop-bucket
scoop install $owner/pipespy
\`\`\`

Or download \`pipespy_${version}_windows_amd64.zip\` (or \`_arm64\`), unzip, and put \`pipespy.exe\` on your PATH.

**Other platforms**: pick the archive for your OS/arch below; \`checksums.txt\` lists SHA-256 sums.

EOF
