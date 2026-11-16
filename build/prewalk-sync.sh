#!/usr/bin/env bash
# prewalk-sync.sh: keep the RayenAlex/magpie fork's main and
# prewalk-maintenance branches synced with yetone/magpie's main, in two
# boundaries so CI can run every check on a candidate between them.
#
#   prepare [--origin-url URL] [--upstream-url URL]
#       Fetch the fork and the upstream, take the fork's
#       prewalk-maintenance (or main when the fork has no maintenance
#       branch yet), merge fork main and then upstream main into it and
#       leave HEAD on the candidate. The candidate's sha goes to stdout's first line and to
#       $GITHUB_OUTPUT as prewalk_sha. A merge conflict fails at once; no
#       remote is written here, ever. "Already up to date" makes no empty
#       commit, so an unchanged upstream just re-verifies the same tip.
#
#   advance --sha SHA [--origin-url URL]
#       Push the verified candidate — the sha prepare printed, with HEAD
#       still on it and the tree clean — to both fork branches in one
#       atomic, fast-forward-only push. Never forced: a fork branch that
#       moved ahead of the candidate rejects the run instead of being
#       overwritten, and so does a sha that no longer matches HEAD.
#
# The URLs default to this fork and its upstream. Remotes are pinned under
# the prewalk-fork/prewalk-upstream names, so the script also works in a
# checkout whose origin is something else (the maintenance worktree maps
# them the other way round).
set -euo pipefail

DEFAULT_ORIGIN_URL='https://github.com/RayenAlex/magpie.git'
DEFAULT_UPSTREAM_URL='https://github.com/yetone/magpie.git'
ORIGIN_REMOTE=prewalk-fork
UPSTREAM_REMOTE=prewalk-upstream
MAIN_BRANCH=main
MAINTENANCE_BRANCH=prewalk-maintenance

# the merge commit's author; an environment that set them keeps its values
: "${GIT_AUTHOR_NAME:=prewalk-bot}"
: "${GIT_AUTHOR_EMAIL:=prewalk-bot@users.noreply.github.com}"
: "${GIT_COMMITTER_NAME:=$GIT_AUTHOR_NAME}"
: "${GIT_COMMITTER_EMAIL:=$GIT_AUTHOR_EMAIL}"
export GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL

log() { echo "prewalk-sync: $*" >&2; }
die() { log "ERROR: $*"; exit 1; }

usage() {
	cat >&2 <<EOF
usage:
  $0 prepare [--origin-url URL] [--upstream-url URL]
  $0 advance --sha SHA [--origin-url URL]
EOF
	exit 2
}

# ensure_remote NAME URL: pin the remote's URL whatever the checkout had
ensure_remote() {
	if git remote get-url "$1" >/dev/null 2>&1; then
		git remote set-url "$1" "$2"
	else
		git remote add "$1" "$2"
	fi
}

write_output() { # NAME VALUE — for GitHub Actions, harmless locally
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		printf '%s=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
	fi
}

require_repo() { git rev-parse --git-dir >/dev/null 2>&1 || die 'not inside a git repository'; }

cmd_prepare() {
	local origin_url="$DEFAULT_ORIGIN_URL" upstream_url="$DEFAULT_UPSTREAM_URL"
	shift
	while [ $# -gt 0 ]; do
		case "$1" in
		--origin-url) [ $# -ge 2 ] || usage; origin_url=$2; shift 2 ;;
		--upstream-url) [ $# -ge 2 ] || usage; upstream_url=$2; shift 2 ;;
		*) usage ;;
		esac
	done

	require_repo
	ensure_remote "$ORIGIN_REMOTE" "$origin_url"
	ensure_remote "$UPSTREAM_REMOTE" "$upstream_url"

	log "fetching the fork ($origin_url)"
	git fetch --quiet "$ORIGIN_REMOTE" || die 'fetching the fork failed'
	local fork_main fork_maint base
	fork_main=$(git rev-parse --verify --quiet "refs/remotes/$ORIGIN_REMOTE/$MAIN_BRANCH" || true)
	[ -n "$fork_main" ] || die "the fork has no $MAIN_BRANCH branch"
	fork_maint=$(git rev-parse --verify --quiet "refs/remotes/$ORIGIN_REMOTE/$MAINTENANCE_BRANCH" || true)
	if [ -n "$fork_maint" ]; then
		base=$fork_maint
	else
		base=$fork_main
		log "the fork has no $MAINTENANCE_BRANCH yet; starting the candidate from $MAIN_BRANCH"
	fi

	log "fetching the upstream ($upstream_url)"
	git fetch --quiet "$UPSTREAM_REMOTE" || die 'fetching the upstream failed'
	local upstream_main
	upstream_main=$(git rev-parse --verify --quiet "refs/remotes/$UPSTREAM_REMOTE/$MAIN_BRANCH" || true)
	[ -n "$upstream_main" ] || die "the upstream has no $MAIN_BRANCH branch"

	git checkout --quiet --detach "$base" \
		|| die "cannot detach onto the candidate base $base"

	log "merging fork $MAIN_BRANCH ($fork_main) into the candidate"
	if ! git merge --no-edit "$fork_main" >&2; then
		die 'merge conflict or failure while integrating fork main; no remote was changed'
	fi

	log "merging upstream $MAIN_BRANCH ($upstream_main) into the candidate"
	# merge's summary ("Already up to date." and friends) goes to stdout by
	# default; stdout here is the sha contract, so it goes to stderr
	if ! git merge --no-edit "$upstream_main" >&2; then
		local conflicted
		conflicted=$(git diff --name-only --diff-filter=U)
		if [ -n "$conflicted" ]; then
			die "merge conflict with the upstream; resolve it by hand in a local clone and push both branches, then let the next run verify the result. Conflicted files:
$conflicted"
		fi
		die 'git merge failed (see its output above)'
	fi

	local candidate
	candidate=$(git rev-parse HEAD)
	log "candidate ready: $candidate"
	log "  fork $MAIN_BRANCH: $fork_main"
	log "  fork $MAINTENANCE_BRANCH: ${fork_maint:-none}"
	log "  upstream $MAIN_BRANCH: $upstream_main"
	echo "$candidate"
	write_output prewalk_sha "$candidate"
}

cmd_advance() {
	local sha='' origin_url="$DEFAULT_ORIGIN_URL"
	shift
	while [ $# -gt 0 ]; do
		case "$1" in
		--sha) [ $# -ge 2 ] || usage; sha=$2; shift 2 ;;
		--origin-url) [ $# -ge 2 ] || usage; origin_url=$2; shift 2 ;;
		*) usage ;;
		esac
	done
	[ -n "$sha" ] || usage
	[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die 'the candidate sha must be a full 40-hex sha'

	require_repo
	git rev-parse --verify --quiet "$sha^{commit}" >/dev/null \
		|| die "unknown commit $sha"

	local head
	head=$(git rev-parse HEAD)
	[ "$head" = "$sha" ] \
		|| die "HEAD ($head) is not the verified candidate ($sha); rerun prepare and the checks"
	git diff --quiet --cached || die 'the tree has staged changes since the checks'
	git diff --quiet || die 'the tree has tracked changes since the checks'

	ensure_remote "$ORIGIN_REMOTE" "$origin_url"
	log "fetching the fork to re-check its branches ($origin_url)"
	git fetch --quiet "$ORIGIN_REMOTE" || die 'fetching the fork failed'

	local branch remote_sha
	for branch in "$MAIN_BRANCH" "$MAINTENANCE_BRANCH"; do
		remote_sha=$(git rev-parse --verify --quiet "refs/remotes/$ORIGIN_REMOTE/$branch" || true)
		if [ -n "$remote_sha" ] && ! git merge-base --is-ancestor "$remote_sha" "$sha"; then
			die "fork $branch moved ahead of the candidate ($remote_sha); rerun prepare and the checks instead of overwriting it"
		fi
	done

	log "pushing $sha to $MAIN_BRANCH and $MAINTENANCE_BRANCH (atomic, fast-forward only)"
	git push --atomic "$ORIGIN_REMOTE" \
		"$sha:refs/heads/$MAIN_BRANCH" \
		"$sha:refs/heads/$MAINTENANCE_BRANCH" \
		|| die 'push rejected; nothing was overwritten — rerun prepare and the checks'
	log "fork $MAIN_BRANCH and $MAINTENANCE_BRANCH are at $sha"
}

case "${1:-}" in
prepare) cmd_prepare "$@" ;;
advance) cmd_advance "$@" ;;
*) usage ;;
esac
