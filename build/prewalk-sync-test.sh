#!/usr/bin/env bash
# prewalk-sync-test.sh: behavior tests for prewalk-sync.sh. Each scenario
# builds its own throwaway world under one temp dir — a bare upstream repo
# standing in for yetone/magpie, a bare fork standing in for
# RayenAlex/magpie, a work clone of the fork the script runs in, and (for
# the race scenario) a second clone playing a competing client. Nothing
# outside the temp dir is read or written; there is no network.
#
# Covered: no change upstream (no empty commit), a clean merge, a conflict
# that fails prepare and writes no remote, a fork branch that moved ahead
# and rejects advance, a fork without prewalk-maintenance yet, and a
# candidate whose HEAD moved past the sha advance was given.
set -u

SYNC="$(cd "$(dirname "$0")" && pwd)/prewalk-sync.sh"

PASS=0
FAIL=0
ROOT=''

cleanup() {
	[ -n "$ROOT" ] && { chmod -R u+w "$ROOT" 2>/dev/null; rm -rf "$ROOT"; }
}
trap cleanup EXIT

note()   { echo "  ok   $1"; PASS=$((PASS + 1)); }
failed() { echo "  FAIL $1" >&2; FAIL=$((FAIL + 1)); }

# check DESC EXPECTED ACTUAL
check() {
	if [ "$2" = "$3" ]; then note "$1"; else failed "$1 — expected [$2], got [$3]"; fi
}

# check_status DESC EXPECTED_RC ACTUAL_RC
check_status() {
	if [ "$2" -eq "$3" ]; then note "$1"; else failed "$1 — expected exit $2, got $3"; fi
}

# check_text DESC NEEDLE TEXT — the failure must name its cause, not just fail
check_text() {
	if printf '%s' "$3" | grep -qi "$2"; then note "$1"; else failed "$1 — [$3] does not mention /$2/"; fi
}

# check_bool DESC RC — for `git merge-base --is-ancestor` and `cat-file -e`
check_bool() {
	if [ "$2" -eq 0 ]; then note "$1"; else failed "$1"; fi
}

W=''
new_world() { # SCENARIO
	W="$ROOT/$1"
	mkdir -p "$W/home"
	export HOME="$W/home"
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
	export GIT_AUTHOR_NAME=sync-test GIT_AUTHOR_EMAIL=sync-test@example.com
	export GIT_COMMITTER_NAME=sync-test GIT_COMMITTER_EMAIL=sync-test@example.com
	git init -q --bare -b main "$W/upstream.git"
	git init -q -b main "$W/upstream-work"
	echo 'base readme' >"$W/upstream-work/README.md"
	git -C "$W/upstream-work" add -A
	git -C "$W/upstream-work" commit -q -m base
	git -C "$W/upstream-work" remote add origin "$W/upstream.git"
	git -C "$W/upstream-work" push -q origin main
	git clone -q --bare "$W/upstream.git" "$W/fork.git"
	git clone -q "$W/fork.git" "$W/work"
}

up_commit() { # FILE TEXT MSG — a commit on the upstream's main
	echo "$2" >"$W/upstream-work/$1"
	git -C "$W/upstream-work" add -A
	git -C "$W/upstream-work" commit -q -m "$3"
	git -C "$W/upstream-work" push -q origin main
}

fork_commit() { # FILE TEXT MSG — a commit on the work clone's HEAD
	echo "$2" >"$W/work/$1"
	git -C "$W/work" add -A
	git -C "$W/work" commit -q -m "$3"
}

push_fork() { # BRANCH... — push the work clone's HEAD to fork branches
	local refs=() b
	for b in "$@"; do refs+=("HEAD:refs/heads/$b"); done
	git -C "$W/work" push -q origin "${refs[@]}"
}

fork_ref() {
	if git -C "$W/fork.git" show-ref --verify --quiet "refs/heads/$1" 2>/dev/null; then
		git -C "$W/fork.git" rev-parse "refs/heads/$1"
	else
		echo "refs/heads/$1"
	fi
}
fork_has_ref() { git -C "$W/fork.git" show-ref --verify -q "refs/heads/$1"; }

# run_prepare: in the work clone; sets PREPARE_RC / PREPARE_OUT / PREPARE_ERR
run_prepare() {
	PREPARE_OUT=$(cd "$W/work" && "$SYNC" prepare \
		--origin-url "$W/fork.git" --upstream-url "$W/upstream.git" 2>"$W/prepare.err")
	PREPARE_RC=$?
	PREPARE_ERR=$(cat "$W/prepare.err" 2>/dev/null || true)
}

# run_advance SHA: sets ADVANCE_RC / ADVANCE_OUT / ADVANCE_ERR
run_advance() {
	ADVANCE_OUT=$(cd "$W/work" && "$SYNC" advance \
		--sha "$1" --origin-url "$W/fork.git" 2>"$W/advance.err")
	ADVANCE_RC=$?
	ADVANCE_ERR=$(cat "$W/advance.err" 2>/dev/null || true)
}

candidate_of() { printf '%s' "$PREPARE_OUT" | head -1; }

scenario_no_change() {
	echo 'scenario: nothing new upstream — the candidate is the branch tip, no empty commit'
	new_world no-change
	local base
	base=$(fork_ref main)
	push_fork prewalk-maintenance
	run_prepare
	check_status 'prepare exits 0' 0 "$PREPARE_RC"
	check 'prepare prints the current tip as the candidate' "$base" "$(candidate_of)"
	check 'work HEAD is left on the candidate' "$base" "$(git -C "$W/work" rev-parse HEAD)"
	run_advance "$base"
	check_status 'advance exits 0' 0 "$ADVANCE_RC"
	check 'fork main stays at the candidate' "$base" "$(fork_ref main)"
	check 'fork prewalk-maintenance stays at the candidate' "$base" "$(fork_ref prewalk-maintenance)"
}

scenario_clean_merge() {
	echo 'scenario: an upstream commit merges cleanly into the maintenance branch'
	new_world clean-merge
	fork_commit fork-note.txt 'kept by the fork' 'fork patch'
	push_fork main prewalk-maintenance
	local base
	base=$(fork_ref prewalk-maintenance)
	up_commit upstream-note.txt 'from the upstream' 'upstream change'
	run_prepare
	check_status 'prepare exits 0' 0 "$PREPARE_RC"
	local cand up
	cand=$(candidate_of)
	up=$(git -C "$W/upstream-work" rev-parse HEAD)
	git -C "$W/work" merge-base --is-ancestor "$up" "$cand" 2>/dev/null
	check_bool 'the candidate contains the upstream commit' $?
	git -C "$W/work" merge-base --is-ancestor "$base" "$cand" 2>/dev/null
	check_bool 'the candidate contains the fork patch' $?
	git -C "$W/work" cat-file -e "$cand:fork-note.txt" 2>/dev/null
	check_bool 'the fork file is in the candidate tree' $?
	git -C "$W/work" cat-file -e "$cand:upstream-note.txt" 2>/dev/null
	check_bool 'the upstream file is in the candidate tree' $?
	run_advance "$cand"
	check_status 'advance exits 0' 0 "$ADVANCE_RC"
	check 'fork main is fast-forwarded to the candidate' "$cand" "$(fork_ref main)"
	check 'fork prewalk-maintenance is fast-forwarded to the candidate' "$cand" "$(fork_ref prewalk-maintenance)"
}

scenario_diverged_fork() {
	echo 'scenario: fork main and maintenance both contribute to the candidate'
	new_world diverged
	fork_commit maintenance.txt 'maintenance work' 'maintenance work'
	push_fork prewalk-maintenance
	git -C "$W/work" checkout -q main
	git -C "$W/work" reset -q --hard "$(fork_ref main)"
	fork_commit main.txt 'main work' 'main work'
	push_fork main
	local main_sha maintenance_sha cand
	main_sha=$(fork_ref main)
	maintenance_sha=$(fork_ref prewalk-maintenance)
	run_prepare
	check_status 'prepare merges both fork branches' 0 "$PREPARE_RC"
	cand=$(candidate_of)
	git -C "$W/work" merge-base --is-ancestor "$main_sha" "$cand" 2>/dev/null
	check_bool 'candidate includes fork main' $?
	git -C "$W/work" merge-base --is-ancestor "$maintenance_sha" "$cand" 2>/dev/null
	check_bool 'candidate includes maintenance' $?
}

scenario_conflict() {
	echo 'scenario: a conflicting upstream change fails prepare and writes no remote'
	new_world conflict
	fork_commit README.md 'fork line' 'fork edits the readme'
	push_fork main prewalk-maintenance
	local base
	base=$(fork_ref prewalk-maintenance)
	up_commit README.md 'upstream line' 'upstream edits the readme'
	run_prepare
	check_status 'prepare fails' 1 "$PREPARE_RC"
	check_text 'the failure names the conflict' 'conflict' "$PREPARE_ERR"
	check 'fork main untouched' "$base" "$(fork_ref main)"
	check 'fork prewalk-maintenance untouched' "$base" "$(fork_ref prewalk-maintenance)"
}

scenario_race() {
	echo 'scenario: a fork branch that moved ahead after prepare rejects advance'
	new_world race
	fork_commit fork-note.txt 'kept by the fork' 'fork patch'
	push_fork main prewalk-maintenance
	up_commit upstream-note.txt 'from the upstream' 'upstream change'
	run_prepare
	check_status 'prepare exits 0' 0 "$PREPARE_RC"
	local cand base
	cand=$(candidate_of)
	base=$(fork_ref prewalk-maintenance)
	# a competing client pushes to the fork's main between prepare and advance
	git clone -q "$W/fork.git" "$W/racer"
	git -C "$W/racer" -c user.name=racer -c user.email=racer@example.com \
		commit --allow-empty -q -m 'racing push'
	git -C "$W/racer" push -q origin main
	local racer
	racer=$(git -C "$W/racer" rev-parse HEAD)
	run_advance "$cand"
	check_status 'advance fails' 1 "$ADVANCE_RC"
	check_text 'the rejection names the branch that moved ahead' 'moved ahead' "$ADVANCE_ERR"
	check 'fork main keeps the racing commit' "$racer" "$(fork_ref main)"
	check 'fork prewalk-maintenance untouched (atomic push)' "$base" "$(fork_ref prewalk-maintenance)"
}

scenario_fresh() {
	echo 'scenario: a fork without prewalk-maintenance starts the candidate from main'
	new_world fresh
	local base
	base=$(fork_ref main)
	if fork_has_ref prewalk-maintenance; then
		failed 'scenario setup: the fork starts without prewalk-maintenance'
	else note 'scenario setup: the fork starts without prewalk-maintenance'; fi
	up_commit upstream-note.txt 'from the upstream' 'upstream change'
	run_prepare
	check_status 'prepare exits 0' 0 "$PREPARE_RC"
	check_text 'prepare says it started from main' 'starting the candidate from main' "$PREPARE_ERR"
	local cand
	cand=$(candidate_of)
	run_advance "$cand"
	check_status 'advance exits 0' 0 "$ADVANCE_RC"
	check 'fork main is the candidate' "$cand" "$(fork_ref main)"
	check 'prewalk-maintenance is created at the candidate' "$cand" "$(fork_ref prewalk-maintenance)"
}

scenario_mismatch() {
	echo 'scenario: advance refuses a sha the work clone has moved past'
	new_world mismatch
	fork_commit fork-note.txt 'kept by the fork' 'fork patch'
	push_fork main prewalk-maintenance
	local base
	base=$(fork_ref main)
	up_commit upstream-note.txt 'from the upstream' 'upstream change'
	run_prepare
	check_status 'prepare exits 0' 0 "$PREPARE_RC"
	local cand
	cand=$(candidate_of)
	# an untested local commit lands on top of the verified candidate
	(cd "$W/work" && echo untested >untested.txt && git add untested.txt && git commit -q -m 'untested change')
	run_advance "$cand"
	check_status 'advance fails' 1 "$ADVANCE_RC"
	check_text 'the rejection names the changed candidate' 'not the verified candidate' "$ADVANCE_ERR"
	check 'fork main untouched' "$base" "$(fork_ref main)"
	check 'fork prewalk-maintenance untouched' "$base" "$(fork_ref prewalk-maintenance)"
}

main() {
	ROOT=$(mktemp -d "${TMPDIR:-/tmp}/prewalk-sync-test.XXXXXX")
	echo "test world: $ROOT"
	scenario_no_change
	scenario_clean_merge
	scenario_diverged_fork
	scenario_conflict
	scenario_race
	scenario_fresh
	scenario_mismatch
	echo
	echo "passed: $PASS  failed: $FAIL"
	[ "$FAIL" -eq 0 ]
}

main "$@"
