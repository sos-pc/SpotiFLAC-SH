# Reworking upstream tracking — plan

> **🧭 Plan, 2026-07-30 (rewritten same day — the first draft below turned out to
> propose the wrong fix; see §0).** Every number is measured, not estimated; the
> commands that produced them are inline so they can be re-run. Companion:
> [module-engine.md](module-engine.md) ·
> [dead-code-removal-plan.md](dead-code-removal-plan.md) §7.6, which predicted the
> breakage §2 measures.

## 0. What changed since the first draft

The first version of this plan (superseded, kept below in §7 for the record)
proposed watching the fork with a weekly job that asserted three things — our
patch set still rebases, the shim's call contract still holds, the quality
vocabulary still resolves — and left the actual sync as a manual `git rebase` a
few times a year.

That plan was answering the wrong question. It assumed **the fork has to exist**
and asked how to monitor drift against it cheaply. Pushed on that assumption
("does the app's deployment have to be this manual?", "why pin a version if the
point is to get updates?"), a cheaper answer appeared: **the fork doesn't have to
exist.** It was created for two reasons — carrying `engine/` and two patches to
the core — and neither needs a fork:

- `engine/` is ours; it can live in *our* repo instead of a fork of theirs.
- The core installs from PyPI (`pip install SpotiFLAC`) instead of being cloned
  and built from forked source.
- Of the two patches, one is **already upstream** (§2.3) and the other is small
  enough to carry as a plain `.patch` file, applied at build time.

Once that holds, "does our patch set still apply" stops being a question a cron
job answers on a schedule — **the build fails immediately if it doesn't.** The
assertion is replaced by removing the surface it was asserting about. That is a
strictly better outcome than anything in §7, which is why this rewrite exists.

## 1. The two upstreams, and what each now requires

| Upstream | What it is to us | Tracking need |
|---|---|---|
| [`BartolomeoRusso9/SpotiFLAC-Module-Version`](https://github.com/BartolomeoRusso9/SpotiFLAC-Module-Version) | the code that performs every download | §2 — retire the fork, install from PyPI, patch what's still ours |
| [`spotbye/SpotiFLAC`](https://github.com/spotbye/SpotiFLAC) | origin of our Go code; a port source, never merged | §5 — shrink what's watched to what we still port from |

## 2. The engine dependency — retire the fork

### 2.1 The patch set, measured

```bash
gh api "repos/BartolomeoRusso9/SpotiFLAC-Module-Version/compare/main...sos-pc:main" \
  --jq '.files[]|"\(.status) +\(.additions)/-\(.deletions) \(.filename)"'
```

```
modified  +22/-0  SpotiFLAC/core/signed_session_desktop.py
modified  +30/-0  SpotiFLAC/core/signed_session_mobile.py
added     +45/-0  engine/Dockerfile
added     +38/-0  engine/FORK-MAINTENANCE.md
added     +15/-0  engine/requirements.txt
added    +181/-0  engine/shim.py
```

Four new files (no conflict possible — upstream has no such paths) and two pure
insertions into the core. That second pair is the entire risk surface, and it
is small enough to stop being a fork and start being two `.patch` files applied
at Docker build time via `patch(1)`.

### 2.2 The wheel is complete enough to build on

Downloaded and inspected `spotiflac-1.5.6-py3-none-any.whl` from PyPI (published
by `BartolomeoRusso9`, project URLs point at the exact repo we forked):

```bash
curl -sL -o w.whl "https://files.pythonhosted.org/packages/.../spotiflac-1.5.6-py3-none-any.whl"
python -c "import zipfile; print(len(zipfile.ZipFile('w.whl').namelist()))"
# 98 files
```

It contains `SpotiFLAC/extensions/_bridge.js` and the rest of `extensions/` and
`frontend/` — so `ext:tidal-web`, the fallback that saved a real download in
prod on 2026-07-29, survives a switch away from the fork. `AsyncSpotiFLAC` is
exported (`SpotiFLAC/__init__.py` → `.client`), and every kwarg `shim.py` passes
(`output_dir`, `services`, `quality`, `allow_fallback`, `enrich_metadata`,
`embed_lyrics`, `log_level`) is present in the class body.

### 2.3 One of the two patches is already upstream

```bash
grep -n "TURNSTILE_SOLVER_URL" SpotiFLAC/core/signed_session_*.py   # inside the wheel
# only signed_session_mobile.py
```

Upstream commit `dc35550b` (2026-07-28 02:10) introduces `_solver_grant_async`,
the constant, and the call site — identical to our fork's patch. Our fork was
created 2026-07-25; `dc35550b` landed **three days later**.
The very next commit, `9e26b405`, strips exactly our formatting (four-space
comment alignment, single-line signature) and reformats it — consistent with
having copied the code from the public fork and then run a formatter over it.
No PR or issue from `sos-pc` exists on the upstream repo (checked: `pulls?state=all`
and `issues?creator=sos-pc` both return empty).

**Consequence:** the `signed_session_mobile.py` patch carries zero content going
forward — drop it. Only `signed_session_desktop.py` (+22 lines) remains ours.

Verified by dry-running our current patch set against the extracted 1.5.6 tree:

```bash
patch -p1 --dry-run < ours.patch
# checking file SpotiFLAC/core/signed_session_desktop.py
#   Hunk #2 succeeded at 166 (offset -14 lines)
# checking file SpotiFLAC/core/signed_session_mobile.py
#   Reversed (or previously applied) patch detected! ... Skipping patch.
```

Exactly what §2.3 predicts: the desktop patch still applies cleanly, the mobile
one is already there.

**Decided — no PR.** The mobile patch shows adoption can happen without one:
upstream read the public fork and copied it, unprompted, within three days. If
`signed_session_desktop.py` gets the same treatment, the patch count drops to
zero on its own. Opening a PR would mean asking for that attention rather than
letting it happen the way it already did once — not the posture wanted here.
The patch stays a `.patch` file, applied at build, indefinitely if needed. If
upstream ever adopts it, the file becomes a no-op `patch` skip (as `mobile`'s
would have) and can be deleted at leisure — no urgency either way.

### 2.4 A dependency the wheel doesn't declare

```bash
python -m venv vtest && vtest/Scripts/pip install SpotiFLAC==1.5.6
vtest/Scripts/python -c "from SpotiFLAC import AsyncSpotiFLAC"
# ModuleNotFoundError: No module named 'pydoll'
```

The wheel's metadata declares 13 `Requires-Dist`; the repo's own
`requirements.txt` has 15. The two missing are `nodriver>=0.36` and
`pydoll-python>=2.9.0` — and without them **the package doesn't import at all**,
not just a lazily-used code path. Confirmed by installing both and re-testing:
import succeeds.

**Mitigation:** don't guess or vendor this list. Resolve the installed version at
build time and pull *that tag's* `requirements.txt` from GitHub raw:

```dockerfile
RUN pip install --no-cache-dir "SpotiFLAC<2" \
 && V=$(python -c "import importlib.metadata as m; print(m.version('SpotiFLAC'))") \
 && curl -fsSL "https://raw.githubusercontent.com/BartolomeoRusso9/SpotiFLAC-Module-Version/v${V}/requirements.txt" \
      -o /tmp/upstream-req.txt \
 && pip install --no-cache-dir -r /tmp/upstream-req.txt
```

`curl -f` fails the build if the tag doesn't exist, rather than shipping an
image that can't import its own core. Upstream stays the source of truth for
its own dependency list; we never re-declare it.

Also needed and easy to miss: `patch(1)` is not in `python:3.12-slim` — add it to
the existing `apt-get install` line, or the build fails on an unrelated-looking
error the first time this is assembled.

### 2.5 Pin the artifact, not the source

Upstream tags fast — v1.5.0 → v1.5.6 in eight days. Pinning the pip version in
source plus Dependabot would mean several PRs a week, which fights the goal
instead of serving it.

Build **unpinned** (guarded only by a major-version ceiling, `SpotiFLAC<2`), and
let CI record what it actually resolved:

- resolve the installed version (`importlib.metadata.version`), write it as an
  OCI label and as part of the image tag
- publish both `ghcr.io/sos-pc/spotiflac-engine:X.Y.Z` and `:latest`
- deployment becomes `docker compose pull && up -d` — no `engine-src` checkout,
  no build tooling on the host, no `/staging` directory to create and `chown`
  by hand (the image does it, as it already does today)

This keeps every property that mattered: `docker inspect` shows exactly which
SpotiFLAC version is running (traceability), rollback is pinning one tag in
compose (one line), and nobody reads a PR to get an update.

**Decided — PyPI wheel, not `git+…@vX.Y.Z`.** Checked rather than assumed:

```bash
gh api repos/BartolomeoRusso9/SpotiFLAC-Module-Version/tags --jq '.[0].commit.sha'
# tag v1.5.9 created  2026-07-30T01:53:24Z
curl -s https://pypi.org/pypi/SpotiFLAC/json | jq -r '.releases["1.5.9"][0].upload_time'
# PyPI upload         2026-07-30T01:53:43
```

**19 seconds** between tag and PyPI publish — an automated `build` + `twine
upload` workflow, not a maintainer remembering to do it. No lag risk. And the
build backend is plain `setuptools` (`pyproject.toml`
`[build-system] requires = ["setuptools>=61.0"]`), so a source install would
just redo, once per image build, exactly what their CI already did once to
produce the wheel — for a result already verified identical (§2.2). The one
edge the git+tag form had — pointing at a real, inspectable ref — is arguably
weaker than it sounds: a git tag can be moved or deleted, a published PyPI
release cannot, and PyPI hands you a `sha256` to pin against
(`pip install --require-hashes`) that a tag doesn't.

This also closes the Dependabot question by making it moot: Dependabot support
for VCS refs only mattered if we were going to pin a tag in source and let a
bot bump it — exactly the frequent-PR outcome §2.5 exists to avoid. Nothing
here goes through Dependabot; the CI-resolves-and-tags-the-image mechanism above
is the whole update path.

**Decided — poll PyPI, gate the build on its own published label.** There is no
native cross-repo push trigger for a third party's release (GitHub cannot fire a
workflow in *our* repo from an event in *theirs*), so "reactive" still means
polling — the only question is what it polls and how cheaply.

Chose PyPI's JSON API over GitHub's `releases/latest`: it's what we actually
install (§2.5 already settled that), needs no auth at all, and is one HTTP
call. Compared against the label our own last-published image already carries —
no separate state file, because the artifact *is* the state, same principle as
the version pin itself.

```yaml
on:
  schedule: [{cron: "*/30 * * * *"}]   # GitHub warns cron can slip under load;
  workflow_dispatch: {}                # 30 min is comfortably above that noise

jobs:
  check:
    runs-on: ubuntu-latest
    outputs: {stale: ${{ steps.cmp.outputs.stale }}}
    steps:
      - id: upstream
        run: echo "v=$(curl -s https://pypi.org/pypi/SpotiFLAC/json | jq -r .info.version)" >> "$GITHUB_OUTPUT"
      - id: current
        run: |
          v=$(docker buildx imagetools inspect ghcr.io/sos-pc/spotiflac-engine:latest \
                --format '{{json .Config.Labels}}' 2>/dev/null \
                | jq -r '.["org.opencontainers.image.version"] // "none"')
          echo "v=$v" >> "$GITHUB_OUTPUT"
      - id: cmp
        run: echo "stale=$([ '${{ steps.upstream.outputs.v }}' != '${{ steps.current.outputs.v }}' ] && echo true || echo false)" >> "$GITHUB_OUTPUT"

  build:
    needs: check
    if: needs.check.outputs.stale == 'true'
    # ... resolve, patch, build, tag with steps.upstream's version, push ...
```

Verified the read side end-to-end against a real public image, over raw HTTP —
the same protocol `docker buildx imagetools inspect` speaks, so proving it here
de-risks using the friendlier command in the actual runner (which ships with
buildx preinstalled, so no extra tooling to add):

```bash
TOKEN=$(curl -s "https://ghcr.io/token?service=ghcr.io&scope=repository:sos-pc/spotiflac-sh:pull" | jq -r .token)
# manifest (index) → per-arch manifest → config blob → .config.Labels
# confirmed readable anonymously: org.opencontainers.image.version = "3.9.0"
```

Also confirmed: our existing GHCR packages (`spotiflac-sh`, `cleanup-linker`)
are public, so `spotiflac-engine` will be too by default — the check job needs
no credential.

The check job costs two cheap HTTP calls and no image pull; the expensive job
(resolve + patch + build + push) only runs when they disagree. Within roughly
30 minutes of a PyPI publish — itself ~19 s after upstream tags, per §2.5 — the
image is rebuilt. First run (no `:latest` yet) falls out naturally: `current`
resolves to `"none"`, which never equals a real version, so it builds.

### 2.6 What this replaces from §7

The old plan's three cron assertions (rebase applies / shim contract holds /
quality vocabulary resolves) all existed to catch drift **against a fork that no
longer exists** here. Their job is now done at build time instead of on a
schedule: `patch` fails loudly if the desktop patch stops applying, the pinned
`requirements.txt` fetch fails loudly if a tag disappears, and the download smoke
test §7 kept manual stays exactly that — manual, run before adopting a new
resolved version, never on cron (a job that fails for someone else's reasons
trains you to ignore it).

## 3. What already shipped, independent of this plan

The fork still exists today, and one real bug in it was fixed and pushed ahead
of any of the above:

**`_prime_tidal_apis()`, `engine/shim.py`, commit `a1f273d`.** Every Tidal
download through the engine failed with `[tidal] UNAVAILABLE: no Tidal APIs
configured`, four times over (once per quality tier, on a condition that can't
vary by tier). Traced to: `PROVIDER_REGISTRY` constructs `TidalProvider`
directly, never through the async factory that refreshes its API list; the
hardcoded seed list is empty; the gist that used to backfill it is 404; the one
code path that *does* refresh it is a background thread we disable with
`enrich_metadata=False`. Fix: call the priming function explicitly, dispatching
on its signature since it is sync in the fork we run today and async from 1.5.6
on. Verified against the live endpoint registry, both signatures:

```
before priming : RuntimeError: No cached Tidal API URLs
after priming  : 2 URLs — api.zarz.moe, tdl-oss.spotbye.qzz.io
```

This does not promise a working Tidal download — it turns "never attempted"
into "attempted against two live endpoints." Full detail in the commit message.

## 4. Verified and ready, pending the go-ahead

**Merging the fork's backlog.** As of this rewrite the fork is 26 commits behind
`BartolomeoRusso9/main` (up from 17 measured earlier the same day — upstream
doesn't sit still, which is itself the argument for not doing this by hand on a
schedule). Dry-run:

```bash
git fetch upstream main && git merge --no-commit --no-ff upstream/main
# Auto-merging SpotiFLAC/core/signed_session_desktop.py
# CONFLICT (content): Merge conflict in SpotiFLAC/core/signed_session_mobile.py
git merge --abort
```

One conflict, and it is cosmetic — both sides add the same constant and
function, differing only in whitespace and line wrapping (consistent with §2.3:
upstream has our content, just reformatted). `desktop.py` merges clean. Safe to
resolve by taking upstream's side on `mobile.py` (it's their code now) and
proceeding.

**Executed 2026-07-30:** merged (`fb8235a`), pushed to the fork. Upstream had
advanced to 1.5.9 in the meantime (17 → 26 commits behind between the first
measurement and the merge — it does not sit still). The Tidal API list bug
(§3) is confirmed still present, unfixed, in 1.5.9: our patch remains necessary.

## 5. The spotbye dependency — shrink what's watched

Unrelated to the fork question. `.github/upstream-map.txt` (45 entries) and its
duplicated classifier (`check-upstream.sh`, 178 lines, reimplemented inline in
`upstream-check.yml`) model spotbye file-by-file, from when we mirrored its
backend close to 1:1. We no longer do:

```
amazon.go, qobuz.go, qobuz_api.go, qobuz_community.go   → backend/{amazon,qobuz}/   deleted
link_resolver.go, songlink.go, songstats.go             → backend/songlink/        renamed isrclookup/
tidal_community.go                                      → backend/tidal/           subject deleted
```

Eight stale entries, each reporting "MAPPED changed, go look" at a local copy
that doesn't exist, forever — `dead-code-removal-plan.md` §7.6 predicted exactly
this.

**Replace the file with a whitelist of the directories we still actually port
ideas from:** `spotify/` (metadata, TOTP), `meta/` (tagging, MusicBrainz,
lyrics), `util/`, Tidal auth. Nothing else in spotbye is a port candidate any
more — its Qobuz/Amazon/Deezer churn is exactly what item 3–5 of the cleanup
plan deleted from our side on purpose. A four-path whitelist self-maintains the
way the old map couldn't: delete a local package, delete a line.

Not built yet: the whitelist file itself, and whatever's left of the classifier
once it only has four paths to check (likely simple enough to fold into a
single script, replacing both the standalone one and the inline workflow copy).

## 6. Order of work

1. ~~Rewrite this plan~~ ✅ done (this document).
2. ~~Merge the fork's 26-commit backlog~~ ✅ done (§4, `fb8235a`).
3. ~~Open an upstream PR for the desktop patch~~ ❌ decided against (§2.3) —
   carry it silently; adopt passively if upstream ever does what it did with
   `mobile`.
4. Build: `engine/` moved into this repo, `patches/signed_session_desktop.patch`,
   the resolve-then-fetch-requirements Dockerfile step (§2.4), the release-poll
   + build CI workflow (§2.5).
5. Switch the example compose from `build: ./engine-src` to `image:
   ghcr.io/sos-pc/spotiflac-engine`.
6. Replace `upstream-map.txt` + the duplicated classifier with the spotbye
   whitelist (§5).

## 7. Superseded first draft (kept for the record)

The original proposal was a weekly job on the *fork* asserting three things,
with the actual sync left as an occasional manual command. §0 explains why this
was replaced rather than built. Kept here because the reasoning that ruled it
out (the risk surface it wanted to catch turned out to be removable, not just
detectable) is worth being able to point back to.

<details>
<summary>Original §3.1–3.4 (click to expand)</summary>

### 3.1 Fork watch — assert, don't review

For a runtime dependency we do not need to read upstream's diff. We need to know
whether our patch set still applies and whether the contract we call still
holds. Both are mechanical.

Weekly job, three assertions:

| # | Assertion | How | On failure |
|---|---|---|---|
| A1 | our patch set rebases onto upstream/main | scratch clone, `git rebase`, check exit code | issue naming the file |
| A2 | the shim's call contract still exists | `inspect.signature` on `AsyncSpotiFLAC`, assert our kwargs | issue naming the missing kwarg |
| A3 | the quality vocabulary still resolves as assumed | call `normalize_quality` on our 6 canonical names | issue naming the value that moved |

Deliberately not automated: pushing the rebase needs a cross-repo PAT, which is
a durability liability the two-minutes-a-few-times-a-year saving doesn't
justify. Not included: a download smoke test on cron, because it fails for
upstream's reasons and trains you to ignore the job — kept as a manual
`workflow_dispatch` instead.

### 3.2–3.4

Baseline auto-advance, a `DELEGATED` map status, and collapsing the duplicate
classifier — all written for the *spotbye* map, and folded into §5 above
unchanged in substance.

</details>
