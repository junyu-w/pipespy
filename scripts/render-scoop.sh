#!/usr/bin/env bash
# Render the Scoop manifest for a release from its checksums.txt.
#   scripts/render-scoop.sh <github-owner> <version> <checksums.txt>
set -euo pipefail
owner=$1
version=${2#v}
sums=$3
base="https://github.com/$owner/pipespy/releases/download/v$version"
hash() {
  local h
  h=$(awk -v f="pipespy_${version}_windows_$1.zip" '$2 == f { print $1 }' "$sums")
  [ -n "$h" ] || { echo "no checksum for windows_$1 in $sums" >&2; exit 1; }
  echo "$h"
}
amd64=$(hash amd64)
arm64=$(hash arm64)
cat <<JSON
{
    "version": "$version",
    "description": "TUI that shows the data flowing through every pipe of a shell pipeline",
    "homepage": "https://github.com/$owner/pipespy",
    "license": "MIT",
    "architecture": {
        "64bit": {
            "url": "$base/pipespy_${version}_windows_amd64.zip",
            "hash": "$amd64",
            "extract_dir": "pipespy_${version}_windows_amd64"
        },
        "arm64": {
            "url": "$base/pipespy_${version}_windows_arm64.zip",
            "hash": "$arm64",
            "extract_dir": "pipespy_${version}_windows_arm64"
        }
    },
    "bin": "pipespy.exe",
    "checkver": {
        "github": "https://github.com/$owner/pipespy"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/$owner/pipespy/releases/download/v\$version/pipespy_\$version_windows_amd64.zip",
                "extract_dir": "pipespy_\$version_windows_amd64"
            },
            "arm64": {
                "url": "https://github.com/$owner/pipespy/releases/download/v\$version/pipespy_\$version_windows_arm64.zip",
                "extract_dir": "pipespy_\$version_windows_arm64"
            }
        },
        "hash": {
            "url": "\$baseurl/checksums.txt"
        }
    }
}
JSON
