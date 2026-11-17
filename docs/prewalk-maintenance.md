# Prewalk maintenance

This fork (RayenAlex/magpie) keeps `main` and `prewalk-maintenance` synced
with yetone/magpie's `main` and publishes a checked image as
`ghcr.io/rayenalex/magpie:latest`, with `prewalk-latest` retained as a
compatibility alias. Both rolling tags follow the same verified digest.
Production Compose uses `:latest`, not a pinned digest; a later update still
requires pulling the tag and recreating the service. This pipeline does not
automatically deploy to production.

## What runs, and in what order

The [Prewalk sync workflow](../.github/workflows/prewalk.yml) runs on every
push to `main` or `prewalk-maintenance`, every 6 hours (`17 */6 * * *`), and
on demand. One run does everything, in this order:

1. **prepare** (`build/prewalk-sync.sh prepare`) fetches the fork and the
   upstream, takes the fork's `prewalk-maintenance` (or `main` on a fork
   that has no maintenance branch yet), merges the fork's `main` and then
   yetone/magpie's `main`, and leaves HEAD on the candidate. A conflict
   fails the run here.
2. **The sync script's own tests** (`bash build/prewalk-sync-test.sh`) run
   against throwaway local repositories — no network, nothing outside a
   temp dir: no change upstream, a clean merge, diverged fork branches, a
   conflict that writes no remote, a racing push that advance rejects, a
   fork without a maintenance branch, and a candidate whose HEAD moved past
   its sha.
3. Patch-scoped checks, under an isolated HOME with Go's caches pinned:
   gofmt on just the Go files the fork's patch changes (its diff against
   upstream main after prepare), `go vet -tags nogui` on the packages it
   touches, and the fork's own patch tests by name —
   `TestPluginForwardGrantBoundaries` and `TestPluginForwardUsageThroughHost`
   (the real loop through the plugin host and the LAN guard: real Bun, two
   caller keys, one usage ledger entry) in `internal/gateway`,
   `TestProviderWrapperKeepsAuditButCountsNoConsumption` in `internal/usage`,
   `TestProviderWrapperPreservesCallerTokenBudget` in `internal/budget`, and
   `TestContextCountsProviderForwardingChildOnce` and
   `TestPluginPageReadsGroups` in `internal/gui`. Their JSON results must
   show each test passing with no skipped test or subtest — without Bun the
   loop test skips, and that fails the run rather than quietly not running.
   Bun 1.4.2 is checked by version first.
4. A local single-arch Docker build is smoke-tested with an ephemeral
   gateway key created through `gateway-key add` in the disposable volume:
   `/v1/models` answers 401 without a key and 200 with it, and an unknown
   model answers 4xx with an `error.type`. The host-to-container request is
   remote, so the loopback-only default token is not valid here.
5. The multi-platform image (`linux/amd64,linux/arm64`, QEMU) is pushed with
   the candidate tag `prewalk-<sha>`, built from the same candidate sha.
   Publication uses the build output digest, not a later tag lookup; GHCR
   tags themselves are mutable and a repeat build can replace this tag.
6. **advance** (`build/prewalk-sync.sh advance --sha <sha>`) pushes that
   same sha to both `main` and `prewalk-maintenance` in one atomic,
   fast-forward-only push.
7. `latest` and `prewalk-latest` are moved to the candidate's digest
   (`docker buildx imagetools create`), and each tag's digest is compared.

Everything is one workflow because a push made with `GITHUB_TOKEN` triggers
no other workflow: no later run would pick the merge up, so this run does
the checks itself.

## Branch rules and failure edges

- **The checks are patch-scoped on purpose** (the owner's decision): this
  fork gates its own patches only. Upstream's own problems stay with
  upstream's maintainers, so no whole-tree test or vet run happens here and
  a red elsewhere in the tree does not stop this pipeline.
- **prepare never writes a remote.** advance is the only push, and it
  happens after every check has passed.
- **advance is atomic and fast-forward only, never forced.** A fork branch
  that moved ahead of the candidate rejects the run instead of being
  overwritten; so does a sha that no longer matches HEAD. This is not an
  atomic compare against the remote snapshot from prepare: a branch
  force-reset to an older ancestor can be advanced again. The guard covers
  ordinary concurrent advances, not remote-history rewrites.
- **A merge conflict fails the run at once** and names the conflicted
  files. Neither branch moves and both rolling tags stay where they were.
  Resolving it is manual, by design — see below.
- **No change upstream is not an error:** the candidate is the branch tip
  (no empty commit is made), the checks rerun, the same sha is re-pushed
  and both rolling tags are re-pointed. This is how a broken first round gets
  rebuilt and repaired.
- **A failed candidate build may leave an orphaned `prewalk-<sha>` tag** in
  GHCR. Neither rolling tag is moved before the advance step.
- The workflow has `if: github.repository == 'RayenAlex/magpie'`: the
  upstream (yetone/magpie) must never run it — it would merge the repo into
  itself and push over yetone's own branches and image. The one concurrency
  group `prewalk-sync` does not cancel in progress, so a run either
  finishes or queues; a cancelled run is not a pass.

## Resolving a conflict by hand

In a local clone of the fork: merge `https://github.com/yetone/magpie`
`main` into `prewalk-maintenance`, resolve, and push. Pushing one branch is
enough: the next run's prepare integrates whichever fork branch you pushed
(`main` or `prewalk-maintenance`) into the candidate, the checks verify it,
and advance leaves both branches at the same sha. There is no auto-rebase
and no automatic conflict resolution; a conflict stays red until a person
resolves it.

## First-time setup on a fresh fork

- **Enable Actions by hand** once (Actions tab → enable). A fork's
  workflows are disabled until then; nothing runs before that.
- The workflow's own `permissions` grant `contents: write` (advance pushes
  the branches) and `packages: write` (GHCR). No PAT, no extra secrets:
  everything uses `GITHUB_TOKEN`.
- **Scheduled runs happen only on the repository's default branch.** Keep
  the default branch on `main` for the 6-hour sync to fire.
- **GHCR visibility is not set automatically.** The first push creates the
  package bound to this repository's settings; it stays private until you
  decide otherwise in the package's settings. Making it public is a manual
  decision, not something this pipeline does.
- **Failure notifications** for scheduled runs come from your GitHub
  notification settings (Watching → Actions), not from the workflow.

## Local tests

`bash build/prewalk-sync-test.sh` covers the script's git behavior with
throwaway local repositories — no network, nothing outside a temp dir: no
change upstream (no empty commit), a clean merge, diverged fork branches,
a conflict that writes no remote, a racing push that advance rejects, a fork
without a maintenance branch, and a candidate whose HEAD moved past its sha.
A branch force-reset to an older ancestor is a remote-history rewrite,
outside this pipeline's boundary.

What was verified locally: the script tests above (37 checks) and the YAML
parsing of the workflow. What was not: the workflow itself on GitHub
runners — the first run on the fork (schedule or `workflow_dispatch`), the
GHCR push, and the cloud smoke test are unverified until Actions is enabled
and the first run is green.
