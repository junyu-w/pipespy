#!/bin/sh
# Render the Homebrew formula for a release.
#
#   scripts/render-formula.sh <github-user> <version>        # uses the GitHub tag tarball
#   scripts/render-formula.sh --local <tarball> <version>    # local testing
set -eu
cd "$(dirname "$0")/.."

if [ "$1" = "--local" ]; then
  tarball=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
  version=$3
  url="file://$tarball"
  homepage="https://example.com/pipespy"
  head="file://$(pwd)"
  sha=$(shasum -a 256 "$tarball" | cut -d' ' -f1)
else
  user=$1
  version=$2
  homepage="https://github.com/$user/pipespy"
  url="$homepage/archive/refs/tags/v$version.tar.gz"
  head="$homepage.git"
  sha=$(curl -fsSL "$url" | shasum -a 256 | cut -d' ' -f1)
fi

sed -e "s|@HOMEPAGE@|$homepage|" -e "s|@URL@|$url|" -e "s|@SHA256@|$sha|" \
    -e "s|@VERSION@|$version|" -e "s|@HEAD@|$head|" packaging/pipespy.rb.tmpl
