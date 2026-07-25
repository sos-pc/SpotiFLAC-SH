# SpotiFLAC Module Version — Integration Plan

Plan for reusing [`BartolomeoRusso9/SpotiFLAC-Module-Version`](https://github.com/BartolomeoRusso9/SpotiFLAC-Module-Version)
**at minimum as a source of providers and track-matching**, and at most as the
full download engine behind our existing Go service.

> **Status: PLANNING — nothing built yet.** This document records the decision,
> the risks, and a reversible phased path. It deliberately keeps the honest
> downsides in view (see [§7 Risks](#7-risks--caveats-read-this)) rather than
> only the upside. Scope and forms are still open — see
> [§8 Open decisions](#8-open-decisions-must-be-answered-before-coding).
>
> **Update 2026-07-22 — Phase 0 done + decisions locked.** The module has **no HTTP
> server** (`app.py` is a GUI launcher; the container runs the CLI or a Telegram bot),
> so the two-service design needs a thin HTTP shim we author around `AsyncSpotiFLAC`.
> Locked: (1) form = **C3** (two-service compose: module + shim); (2) **Tidal BYOT**
> credential-override (native path wins when a valid token is present, else delegate);
> (3) delegating the community-proxy providers **may retire our Selenium/Turnstile solver** —
> ⚠️ *downgraded from "retires" on 2026-07-23: one engine route still prompts for a manual grant,
> see §6;*
> (4) workspace = **new long-lived branch `feat/module-engine`**, not a new repo.

Related: [EXTERNAL_APIS.md](EXTERNAL_APIS.md) · [CREDITS.md](../CREDITS.md) · [tidal-auth.md](tidal-auth.md)

## Reconciliation with prior dev2 work (2026-07-23)

This plan is not a blank slate — dev2 already carried related work. Reconciled:

- **Supersedes (now archived):** [provider-matching-investigation.md](archive/provider-matching-investigation.md)
  §7 (the MusicBrainz→Qobuz matching pipeline, in Go) and
  [external-api-layer.md](archive/external-api-layer.md) (the plan to build/verify the community
  Qobuz/Amazon/DRM layer **ourselves**). The engine does resolution + matching internally and owns
  the provider layer, so **neither is implemented.**
- **Builds on (done):** the service-selection refonte
  ([archive/override-rework-plan.md](archive/override-rework-plan.md), verified in prod). Two
  consequences that **correct earlier drafts of this plan**:
  - The service-rewrite **override in `jobs_helpers.go` is already removed.** The engine plugs into
    the existing, clean `ExecuteDownload` fallback chain (`backend/downloader.go`, ~line 441) — that
    loop **is** the integration point, not something we build.
  - The `autoOrder` default is **aligned across front/back** (`defaultAutoOrder` ==
    `DEFAULT_AUTO_ORDER`) — but it is **`tidal-qobuz-amazon-deezer`, i.e. still TIDAL-FIRST**
    (`backend/downloader.go:33`, verified 2026-07-23). ⚠️ *A previous revision of this note claimed
    the order was "already non-tidal-first, point moot" — that was wrong.* The original concern
    stands: **with no BYOT token, a tidal-first chain burns the first attempt on a 30 s preview.**
    Action kept: when no valid Tidal token is present, the anonymous chain must lead with
    real-FLAC sources (qobuz/deezer/amazon).
- **Not a new discovery:** getting the ISRC straight from Spotify is `spotify.GetTrackISRC` /
  `GetISRCDirect` (`backend/spotify/identifiers.go`), already in dev2 and documented in the archived
  investigation. The BYOT-Tidal path reuses it.

**Net:** the engine plan replaces two shelved provider-layer plans and slots into an
already-refactored dispatch — **less to build than the migration map's raw "what dies" list implies**
(the override is already gone).

---

## 1. Goal & scope

Use the Module Version to fill the provider/matching gaps our Go backend cannot
currently cover well, **without** abandoning our service layer (auth, watcher,
catalog, SSE, M3U8, multi-user, admin).

Two things we want from it, in priority order:

1. **Matching / resolution logic** — how it turns a Spotify track into the right
   Qobuz/Tidal/… track *without depending on ISRC or Song.link*. This is
   extractable knowledge we can adopt natively (see [§4](#4-key-finding-their-matching-strategy)).
2. **Providers we can't do ourselves today** — Amazon (DRM), Deezer (our
   endpoint is dead), and optionally the long tail.

"At least as a source of provider/matching" is the floor. Running it as a live
download engine (sidecar) is the ceiling. The plan supports stopping anywhere on
that spectrum.

---

## 2. What the Module Version is (facts)

| Property | Value |
|---|---|
| Language | Python 3.12 (async), + JavaScript extension fallback system (needs Node.js) |
| Shape | Library (`SpotiFLAC` sync / `AsyncSpotiFLAC`), one-shot CLI, GUI, Docker image, prebuilt standalone binaries |
| Providers (native `.py`) | tidal, qobuz, amazon, deezer, soundcloud, youtube, apple_music, pandora, gdstudio, songstats, soundplate |
| Credentials required | **None** for basic operation — anonymous Spotify GraphQL + anonymous provider search |
| Optional inputs | self-hosted Qobuz API (`qobuz_local_api_url`), self-hosted Tidal hifi-api (`tidal_custom_api`), Telegram bot for Cloudflare bypass (`TG_BOT_TOKEN`/`TG_CHAT_ID`) |
| Default behavior | Tags the file, embeds LRC lyrics (`embed_lyrics=True`), enriches via MusicBrainz (`enrich_metadata=True`) |
| Docker image | `python:3.12-slim` + ffmpeg + flac + nodejs, runs as **root**, ~400–500 MB |
| License / activity | MIT · v1.5.0 (2026-07-20) · ~1045 commits · author is anonymous |

**Invocation contract (why integration is even feasible):**

```bash
spotiflac <spotify_url> <output_dir> --service deezer --quality LOSSLESS
```

One-shot, non-interactive, **writes a FLAC to disk**. Input = URL, output = file.
No IPC protocol to invent — our watcher/catalog already stats the disk.

---

## 3. Why now — current provider reality

| Provider | Real status in our Go backend | Gap |
|---|---|---|
| **Tidal** | Works via our personal Premium token | none — keep as is |
| **Qobuz** | Download works (`Success track=Bloomdido`), but **resolution ~80% broken**: ISRC-first search, and Qobuz barely indexes ISRC; Song.link returns no Qobuz link | **matching**, not download |
| **Amazon** | Not working — encrypted `.m4a`, needs DRM handling | download (DRM) |
| **Deezer** | Endpoint dead (`api.deezmate.com` returns HTML) | download |

The single most valuable insight from the module fixes the biggest gap (Qobuz
matching) **and requires no new runtime**.

---

## 4. Key finding: their matching strategy

Confirmed by reading `providers/qobuz.py` and `providers/spotify_metadata.py`.
Their resolution **does not use ISRC and does not use Song.link**:

1. **Metadata** — `spotify_metadata.py` pulls title, artists, album, album_artist,
   duration_ms, track/disc number, release date, cover, composer via Spotify's
   **internal GraphQL** (persisted-query SHA256 hashes), **anonymously**. ISRC is
   left empty (`isrc=""`) — they never rely on it.
2. **Search** — `qobuz.py` calls the signed `track/search` endpoint with a
   **free-text** `query` (`"<artist> <title>"`), the same signed endpoint we
   already call for ISRC search.
3. **Pick** — `_score_track_candidate()` scores each result:

   | Signal | Weight |
   |---|---|
   | Exact normalized title | +1200 |
   | Title substring | +420 |
   | Artist present | +180 |
   | Album present | +100 |
   | ISRC present (not equality) | +15 |
   | 24-bit / ≥88.2 kHz | +10 each |

   Text is normalized (strip diacritics, lowercase, clean special chars) — which
   matters for our accented / punctuation-heavy catalog.

**Takeaway:** the resolution is text + metadata scoring. This is directly
portable to Go against the endpoint we already sign, with **no DRM and no
sidecar** (see Track 1 in [§9](#9-phased-plan)).

---

## 5. Integration options considered

| Option | Mechanic | Verdict |
|---|---|---|
| **A — Fork the module** | Restart in Python, re-implement our service there | ❌ Throws away auth/watcher/catalog/SSE/multi-user. Their repo is an automation lib, not a service. |
| **B — Port every provider to Go** | Re-implement 11 providers + matching in Go, forever chasing their commits | ❌ As a *global* strategy: unmaintainable, and Amazon = DRM the assistant will not author. ✅ *Viable for one thing*: the Qobuz matching strategy (§4). |
| **C — Sidecar (run their code)** | Our Go keeps the service; delegate download to their module via CLI/HTTP + shared disk | ✅ Only sane way to get *many* providers, and the only path to Amazon that keeps the assistant's DRM line intact (their code does the DRM, user installs). |

**Sidecar forms:**

- **C1 — prebuilt binary**: drop their Linux binary, `exec` it. Lightest. Risk: JS-extension providers may need Node the binary might not bundle.
- **C2 — pip in our image**: one container, but our lean Go image balloons to ~500 MB and their root-oriented code must be made to run non-root (uid 1000).
- **C3 — separate container**: their official image beside ours, HTTP on localhost + shared download volume. Cleanest in a homelab compose; their `root` stays confined. **Contract unverified** — depends on whether their container exposes a usable server API (see Phase 0).

---

## 6. Decision (two independent tracks)

The goal splits cleanly, so we pursue it as two tracks that don't block each other:

- **Track 1 — Matching (adopt the strategy, port to Go).** Fixes Qobuz's 80%.
  No new runtime, no DRM, fully within the assistant's line. This is the "source
  of matching" floor of the goal, and it's the highest ROI.
- **Track 2 — Providers (sidecar).** Get Amazon/Deezer/long-tail by running the
  module. Start with a **reversible Deezer pilot** (no DRM) before committing to
  any runtime form. This is the "source of providers" reach of the goal.

Recommended sidecar form: **C3** (two-service compose) — Phase 0 confirmed the module
has no HTTP server, so service B is the module image **plus a thin FastAPI shim** around
`AsyncSpotiFLAC` (see Phase 0 below).

**Possible benefit — MAY retire Selenium (downgraded 2026-07-23, was overstated).** The engine's
download path solves *some* challenges in code (ALTCHA proof-of-work, `/prepare` token exchange)
across a multi-host fallback chain (gdstudio, wjhe.top, squid.wtf, flacdownloader, antra,
community, musicdl).

⚠️ **An earlier revision claimed "no browser, no session grant, so we can delete the solver
entirely." That was wrong** — observed on the first live run: `core/signed_session_mobile`
falls back to an **interactive stdin prompt asking a human to paste a grant**
(`api.zarz.moe/v2/challenge`), i.e. the same class of challenge our Turnstile solver answers.
Some routes are code-solved, **not all**.

Revised position:
- Whether the solver can be deleted **depends on which routes actually carry our traffic**, and is
  decided by prod evidence, not by this doc. First evidence is encouraging: **Qobuz succeeded in
  6 s without any grant** (2026-07-23).
- The solver may turn out to be an **asset rather than dead weight**: it produces exactly the kind
  of grant this path asks for, so feeding it to the engine is an option worth exploring before
  deleting anything.
- **Operational rule:** never give the engine container a TTY (`stdin_open`/`tty` off). Without one
  the prompt fails fast on EOF; with one, a download could hang waiting for human input.

Trade unchanged: more anonymous third-party hosts in the download path (§7.2), but with fallback
resilience our single musicdl.me lacks.

**Foundation reframe — the project must work with NO account (no BYOT).** Anonymous Tidal is
previews-only (the engine's Tidal uses the same hifi-api), so the engine is the **anonymous
foundation**: its real value is turning each provider from a single-point-of-failure proxy into
a self-healing multi-route path — not "more providers." **BYOT (Tidal token, later a Qobuz
account) is an optional quality *override*, not the backbone.** Two things baked in from day one:
1. **Engine-agnostic shim.** The `POST /download` contract does not name Bartolomeo. If this
   upstream dies we repoint the shim at spotbye or another downloader **without touching Go**.
   We couple to "a download engine", not to one author — this is the durability insurance for
   putting the anonymous foundation on a third-party upstream.
2. **Anonymous auto-order leads with Qobuz/Deezer/Amazon**, not Tidal (current default
   `tidal-amazon-qobuz` wastes the first attempt on a 30 s preview when no token is present).
   Tidal is promoted to first only when a valid token exists (BYOT).

---

## 7. Risks & caveats (read this)

These are deliberately kept in the plan, not buried.

1. **Legal.** Anonymous FLAC ripping from Tidal/Qobuz/Amazon/Deezer violates every
   service's ToS. Amazon specifically means **circumventing a technical protection
   measure** (DMCA §1201 / EU equivalents). "Add all providers" = broadening an
   infringement tool. This is a private homelab and the operator's call; the plan
   does not pretend it is neutral.
2. **Supply chain / RCE.** The module is written by an **anonymous** author,
   **auto-installs Node.js**, and **fetches JS "extensions" at runtime** (remote
   code). Its own image runs as **root** with network + our download volume. A
   malicious or compromised update is arbitrary code execution on the server.
   "Actively maintained" is not "trusted." Pin versions; never auto-update.
3. **Upstream fragility.** These tools break constantly (providers patch
   endpoints; DMCA takedowns; author burnout). v1.5.0 "two days ago" is a symptom
   of constant breakage, not just health. Full-sidecar makes our **entire provider
   layer depend on one anonymous upstream** — if it dies, our downloads die, and
   our own Go providers will have atrophied.
4. **Sunk cost.** Full-sidecar makes recent Go work redundant: `backend/qobuz/signed_search.go`,
   `backend/qobuz/community.go`, most of `backend/community/`, and the
   Turnstile-solver wiring. That code becomes dead weight. Track 1 (matching)
   *reuses* our signed endpoint; Track 2 (sidecar) *replaces* it.
5. **DRM boundary (assistant).** The assistant will build all legitimate plumbing
   (exec/HTTP wrapper, disk handoff, catalog wiring, error surfacing, non-root
   hardening, health checks) but **will not author or port the DRM decryption
   itself**. The sidecar works precisely because their code does that part and the
   operator installs it — routing around the refusal does not launder it, and the
   assistant will not pretend otherwise.
6. **Want vs. need.** The real wall today is **two** providers (Amazon, Deezer).
   SoundCloud/YouTube are MP3, Apple is M4A — they dilute the "true FLAC" premise;
   Joox/NetEase/Migu/Kuwo will realistically never be queued. Importing a whole
   runtime + risks 1–3 to go from 10 to 12 checkboxes (half non-FLAC) optimizes a
   number, not a need. Prefer the smallest scope that closes the real gap.

---

## 8. Open decisions (must be answered before coding)

1. **Tagging ownership.** *(RESOLVED — we own tagging.)* Engine tagging/enrichment is
   turned **off** (`enrich_metadata=False`, `embed_lyrics=False`); we **re-tag at
   ingestion** with our pipeline (`SPOTIFY_ID`, genre policy, naming) for catalog +
   M3U8 consistency. Chosen for library control.
2. **CAPTCHA path.** Their Cloudflare bypass is a Telegram bot; our Turnstile-Solver
   already does this. Keep exactly one.
3. **Tidal token.** *(RESOLVED — BYOT credential-override.)* The engine is the default
   download backend; a **valid** personal token promotes our native Go path to priority
   for that provider only (Tidal today, extensible to others). Router lives in our Go:
   token present+valid → Go Tidal (proven full-FLAC); absent/expired/401 → delegate to the
   engine. Pass-through (inject the token via the module's `tidal_custom_api`) stays
   available if we later want a single download path.
4. **Sidecar form.** C1 / C2 / C3 — depends on Phase 0 and on how many providers
   we actually want.
5. **Scope.** Which providers do we actually commit to? (Recommended: Deezer +
   Amazon only, unless a concrete need for more appears.)

---

## 9. Phased plan

Each phase is independently valuable and reversible until the one that adds a runtime.

### Phase 0 — Verify the sidecar contract *(DONE 2026-07-22)*
- Finding: `app.py` is a **GUI launcher**; the container entrypoint dispatches to the
  **CLI** or `telegram_wrapper.py` — **no built-in HTTP server**.
- Decision: service B = module image **+ a ~40-line FastAPI shim** exposing
  `POST /download` and `GET /health`, writing FLAC to a shared volume (C3). Our Go posts
  a per-job `out_dir`, reads the resulting file, and ingests it (naming, tags, catalog).

### Phase 1 — Matching win in Go *(Track 1, no new runtime)*
- Confirm our current Qobuz client: where `searchByISRC` lives, and where
  title/artist/album/duration come from in our metadata pipeline.
- Add a text-search path against the signed `track/search` endpoint and a
  `scoreTrackCandidate` function mirroring §4 (adapted weights, diacritic
  normalization, duration ±2 s guard as an anti-false-positive extra).
- Keep ISRC as a fast-path/tiebreak, not the primary key.
- **Exit:** Qobuz resolution success rate goes from ~20% to high; download path
  unchanged. Fully reversible (additive).

### Phase 2 — Deezer sidecar pilot *(Track 2, reversible, no DRM)*
- Stand up the module (form TBD from Phase 0) for **Deezer only**.
- A Go provider shells out / calls it, reads the resulting FLAC from the shared
  volume, hands it to the existing catalog/watcher, surfaces errors into
  Debug Logs/SSE.
- Neutralize their tagging per decision §8.1.
- **Exit:** end-to-end `Spotify URL → FLAC on disk → catalog → M3U8/SSE` proven
  through the module, with no DRM involved. Tear-down is trivial if it's not worth it.

### Phase 3 — Broaden *(only if Phase 2 is worth it)*
- Lock the sidecar form and tagging ownership.
- Add Amazon as `--service amazon` — **operator installs the module; the DRM
  runs inside their code**; the assistant only wires the plumbing.
- Decide the long tail against §7.6 (probably: don't).

### Phase 4 — Hardening
- Non-root operation (uid 1000 vs. their root default; writable cache/config).
- Version pinning (no auto-update — see §7.2).
- Health check parity in `GET /api/v1/apis/status`.
- Document the superseded Go code paths (§7.4) and either retire or keep as
  fallback deliberately.

---

## 10. What this supersedes / touches

- **Reuses:** the signed Qobuz `track/search` endpoint (Phase 1), our catalog,
  watcher, SSE, auth (all phases).
- **Potentially retires (Phase 3+):** `backend/qobuz/signed_search.go`,
  `backend/qobuz/community.go`, much of `backend/community/`, Turnstile wiring —
  keep only if chosen as a deliberate fallback.
- **Unchanged:** Tidal (personal token), unless §8.3 chooses injection.

---

## 11. Success / exit criteria

- **Floor met** when: Qobuz resolution is fixed in Go (Phase 1) — the "source of
  matching" goal, achieved with zero third-party runtime.
- **Reach met** when: Deezer (and optionally Amazon) download reliably through the
  module (Phase 2–3) — the "source of providers" goal.
- **Abort cleanly** if: Phase 0 shows no usable contract, or the Phase 2 pilot
  shows the operational/security cost (§7) outweighs closing a two-provider gap.
