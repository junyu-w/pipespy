#!/usr/bin/env bash
# One-time setup: give the release workflow write access to the Homebrew tap.
# Creates an SSH deploy key that can write only to <owner>/homebrew-tap and
# stores its private half as the TAP_DEPLOY_KEY secret on <owner>/pipespy.
#   scripts/setup-tap-key.sh [owner]
set -euo pipefail
owner=${1:-junyu-w}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

ssh-keygen -q -t ed25519 -N "" -C "pipespy release" -f "$dir/tapkey"
gh repo deploy-key add "$dir/tapkey.pub" --repo "$owner/homebrew-tap" \
  --allow-write --title "pipespy release workflow"
gh secret set TAP_DEPLOY_KEY --repo "$owner/pipespy" < "$dir/tapkey"
echo "done: deploy key added to $owner/homebrew-tap, secret TAP_DEPLOY_KEY set on $owner/pipespy"
