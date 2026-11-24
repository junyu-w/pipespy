#!/usr/bin/env bash
# One-time setup: give the release workflow write access to a package repo.
# Creates an SSH deploy key that can write only to <owner>/<repo> and stores
# its private half as an Actions secret on <owner>/pipespy.
#
#   scripts/setup-deploy-key.sh homebrew-tap TAP_DEPLOY_KEY
#   scripts/setup-deploy-key.sh scoop-bucket SCOOP_DEPLOY_KEY
set -euo pipefail
repo=${1:?usage: setup-deploy-key.sh <repo> <SECRET_NAME> [owner]}
secret=${2:?usage: setup-deploy-key.sh <repo> <SECRET_NAME> [owner]}
owner=${3:-junyu-w}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

ssh-keygen -q -t ed25519 -N "" -C "pipespy release → $repo" -f "$dir/key"
gh repo deploy-key add "$dir/key.pub" --repo "$owner/$repo" \
  --allow-write --title "pipespy release workflow"
gh secret set "$secret" --repo "$owner/pipespy" < "$dir/key"
echo "done: deploy key added to $owner/$repo, secret $secret set on $owner/pipespy"
