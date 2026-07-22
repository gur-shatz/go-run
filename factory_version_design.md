# Factory version: image-provided fallback version for supervised components

Status: implemented on master (2026-07-22), uncommitted; all 10 design tests in
place (`factory_test.go`, `readonly_baseline_test.go`, `registry_limp_test.go`).
Consumer context: Firegate connector base image (safeapi); design investigation in
`~/.marks/notes/research/2026_07/202607_22_connector_supervisor_base_image_updates.md`.

## Problem

The supervisor has one mutable state root per component. Everything — the running
version, downloaded versions, `current.txt`, `stable.txt`, `rejects.txt` — lives in
the same writable tree. A container that wants to be both "fully baked" (download
and run, works offline) and self-updating cannot express that today:

- Baking a bundle into the state dir (the current `current.txt = "."` convention)
  breaks under a volume mount: an empty volume mounted at the state dir shadows the
  baked content, leaving nothing to run. Seeding an empty volume needs an init step
  that a FROM-scratch image with the supervisor as PID 1 cannot do (no shell).
- `"."` is not a valid version name for the stable/reject/GC machinery, so the baked
  bundle cannot serve as a rollback target once updates are enabled.

## Feature

The A/B firmware pattern: a read-only, image-provided **factory version** outside
the state dir, which the version machinery falls through to.

```yaml
components:
  - name: connector
    command: './connector.bin -c ./connector.yml'
    factory:
      dir: /opt/connector/factory/connector   # bundle files directly (no versions/ nesting)
      version: 260722_113000_master           # its version identity
```

Both fields required together; a component without `factory:` behaves exactly as
today. `dir` is resolved like other config paths (absolute, or relative to the
supervisor.yml directory). Validate at startup: dir exists and is non-empty when
configured; fail config load otherwise (a missing factory is an image build error,
not a runtime condition).

## Semantics

Resolution principle: **the factory version is a version that is always "prepared".**
It participates in the normal state machine by name; only its storage is special.

1. **Version dir resolution.** Wherever a version name is mapped to a directory
   (launch cwd, `versionExtracted`/usable checks, template validation): if
   `v == factory.version`, resolve to `factory.dir`, unconditionally. Downloaded
   state never shadows the factory name — if `required.txt` names the factory
   version, nothing is fetched (`PrepareVersion` is a no-op success) and no
   signature is checked (trust comes from the image, same as the supervisor binary).
2. **First boot / empty state.** In `computeDesiredVersion`, when `current.txt` is
   missing or names an unusable version and no remote target is resolvable (updates
   disabled, or origin unreachable), the desired version is `factory.version`.
   Boot must never block on the origin. `SwitchToVersion` then writes
   `current.txt = factory.version` as usual (the state dir may be a volume; writing
   is fine and makes subsequent boots ordinary).
3. **Stable of last resort.** `DemoteToStable`: when `stable.txt` is absent or names
   an unusable version, demote to `factory.version` instead of retry-with-backoff on
   the rejected version. Do not require the image to pre-write `stable.txt`.
4. **Rejects.** The factory version can be rejected like any other (it is just old).
   If it is rejected AND nothing else is usable, existing behavior applies (retry
   under backoff). `reject_expiry` applies normally.
5. **GC.** `CleanOrphanVersions` operates on `state_dir/versions/` only; the factory
   dir is untouched by construction. No special-casing needed beyond never treating
   the factory name as an orphan candidate (it has no folder there).
6. **Pinning.** `forced_versions.txt` may name `factory.version` → pin to baked.
7. **Read-only dir.** The child's working directory is `factory.dir`, which may be a
   read-only image layer. The supervisor already writes its state files (current/
   stable/rejects/kill.sock, logs) in the component root and logs dir, not the
   version dir — verify no write path targets the version dir, and document that
   components must not write to their cwd (existing convention: data goes to
   BASE_DIR).
8. **Logs.** Log paths are keyed by version name; the factory name is an ordinary
   string, no change.

## Degraded (limp) mode: unusable state dir

Requirement: the container must survive being installed without a writable (or any)
state directory — e.g. `docker run --read-only`, a misconfigured volume, or a full
disk — by running the factory version with reduced function, not by crashing.

Detection: one writability probe at supervisor startup per component root
(create + delete a probe file; treat mkdir-if-missing failure the same). The result
sets a `stateWritable` flag consulted through a small state-store wrapper, so
call sites stay clean and the degradation is centralized, logged once, and visible
on the health console and metrics (`supervisor_state_writable 0`).

Fallbacks per write surface:

1. `supervisor.lock` — skip locking with a warning (single instance per container
   by construction; the lock protects shared-host scenarios).
2. `current.txt` / `stable.txt` / `rejects.txt` — keep the version state machine
   in memory: crash counting, rejection, and backoff work for the session but do
   not survive a supervisor restart (acceptable: a restart lands on factory).
3. `kill.sock` — optional; `StopChild` already degrades kill-socket → SIGTERM →
   SIGKILL, so only graceful-stop finesse is lost.
4. Logs under `state_dir/logs/` — fall back to passthrough child stdout/stderr
   (in containers this is preferable anyway: `docker logs` sees everything).
5. Statemonitor `history_dir` — already optional; run in-memory only.
6. Update installs — impossible without writes: disable image fetch/extract, but
   KEEP the cheap `required.txt` pointer poll so health can report "update
   available, state dir unwritable" — the operator's signal that the install is
   degraded rather than current.

Resolution rule in limp mode: state dir unreadable or empty → factory. Readable but
unwritable with a valid `current.txt` whose version dir is usable → honor it (a
populated read-only state mount keeps running what it last had); otherwise factory.

## Touch points

- `pkg/supervisor/config.go` — `FactoryConfig{Dir, Version}` on `ComponentConfig`,
  validation, path resolution against the config dir.
- `pkg/supervisor/paths.go` — version-dir resolution gains the factory fallthrough
  (single helper so all call sites agree).
- `pkg/supervisor/component.go` — `computeDesiredVersion` (empty/unusable current →
  factory when no remote target), `PrepareVersion` (factory name → no-op),
  `DemoteToStable` (factory as floor).
- `pkg/supervisor/install.go` — skip fetch/verify/extract for the factory name.
- `pkg/supervisor/gc.go` — no change expected; add a test proving factory survives.
- `pkg/supervisor/supervisor.go` + `state.go` — startup writability probe, lock
  skip, state-store wrapper carrying `stateWritable`; log-sink and kill.sock
  fallbacks in the lifecycle/logging paths.

## Tests

1. Empty state dir + factory → component launches from factory, `current.txt`
   written with the factory name (the volume-shadow scenario).
2. Updates enabled, origin unreachable, empty state → factory runs; origin comes up
   later naming a new version → normal download/switch.
3. `required.txt` names the factory version → no HTTP image fetch occurs.
4. New version crash-loops, `stable.txt` empty → demote lands on factory.
5. Factory rejected while a downloaded version is usable → downloaded version wins;
   factory rejected and nothing else usable → retry-with-backoff as today.
6. GC with `version_folder_retention: 0` leaves the factory dir untouched.
7. Component without `factory:` → byte-identical behavior to today (regression).
8. Read-only state dir (or missing, uncreatable) + factory → supervisor starts,
   factory runs, no crash; warnings logged once; `supervisor_state_writable 0`.
9. Read-only but populated state dir with valid `current.txt` → that version runs;
   crash-loop demotes to factory in memory.
10. Limp mode with updates enabled → no image fetch attempted; pointer poll still
    reports the remote target on the health surface.

## Explicit non-goals

- No supervisor self-update; the supervisor binary, supervisor.yml, and the update
  public key stay image-frozen. The update channel must never be able to alter what
  verifies the update channel.
- No config delivery via updates (unchanged); volatile config rides inside the
  component tarball as before.
- Component list stays static from supervisor.yml.

## Consumer (safeapi) follow-up, for reference only

`tools/connectorhub` base image variant: supervisor + `supervisor.yml` (updates
enabled via `FIREGATE_HUB_URL` env) + Ed25519 public key + factory bundle at
`/opt/connector/factory/connector/`; no state-dir content baked at all. Origin
served by connectorhub at `/origin/connector/{versions,images}/...` per the
supervisor's remote contract.
