#!/bin/sh
# Build the REISUB Meeting web client from its main branch and upload it to
# the server, where the backend serves it from ./web (see README, "Web app").
#
#   scripts/deploy-web.sh <ssh-host> [path-to-client-repo]
#
# The build uses a temporary worktree, so the client repo's working tree is
# never touched. No container restart is needed: the backend reads the files
# on each request.
set -eu

host=${1:?usage: scripts/deploy-web.sh <ssh-host> [path-to-client-repo]}
client=${2:-../REISUB_Meeting}
remote_dir=${REMOTE_WEB_DIR:-/root/projects/REISUB_Meeting_Engine/web}

work=$(mktemp -d)
cleanup() {
	git -C "$client" worktree remove --force "$work/src" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

git -C "$client" fetch --quiet origin main
git -C "$client" worktree add --quiet --detach "$work/src" origin/main
echo "Building $(git -C "$work/src" log --oneline -1)"
(cd "$work/src" && flutter pub get && flutter build web --release)
test -f "$work/src/build/web/invite/index.html" || { echo "invite page missing from build" >&2; exit 1; }

ssh "$host" "mkdir -p '$remote_dir'"
# --delay-updates and --delete-after keep the old build intact until every
# new file has arrived.
rsync -rlt --delay-updates --delete-after --chmod=D755,F644 "$work/src/build/web/" "$host:$remote_dir/"
echo "Deployed to $host:$remote_dir"
