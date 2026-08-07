# Archive

Docs that are **no longer live guidance**. Two reasons a doc lands here:

- **✅ Done** — the work shipped and was verified in prod; kept only to understand *why* the
  code is the way it is.
- **⛔ Superseded by the download-engine project** — the approach it planned was replaced by
  delegating downloads to the engine sidecar (see [module-version-integration.md](module-version-integration.md)).
  That project began as a fork; the fork itself was later retired in favour of installing upstream's
  published package plus our patches — see [../upstream-tracking-plan.md](../upstream-tracking-plan.md).

Nothing here describes the current or intended state of the code. For that, start at
[../README.md](../README.md).

| Doc | Why archived |
|---|---|
| [module-version-integration.md](module-version-integration.md) | ✅ Done — the decision to fork a third-party downloader and run it as a sidecar. Shipped; see [../module-engine.md](../module-engine.md) for what was actually built. ⚠️ Its risk section still claims the Turnstile solver becomes removable — production disproved that. |
| [module-engine-migration.md](module-engine-migration.md) | ✅ Superseded — the "what dies from dev2" map. It listed the Qobuz downloader and the solver as removable, and this line used to answer "both are load-bearing today". Half of that flipped: the Qobuz/Amazon/Deezer downloaders **were** removed in v4.0.0, so the map was right about them. The solver was not — it still serves the engine, measured 2026-08-04. |
| [dead-code-removal-plan.md](dead-code-removal-plan.md) | ✅ Done — exécuté par la v4.0.0. `backend/{qobuz,amazon,deezer,community,songlink}` et `util/proxy_config.go` n'existent plus, vérifié fichier par fichier le 2026-08-04. Son en-tête disait encore « nothing removed yet ». Gardé pour l'analyse de dépendances mesurée qui a justifié chaque suppression. |
| [third-party-layer-status.md](third-party-layer-status.md) | 🕰️ Historical — a live snapshot (2026-07-15/18) of the community-proxy layer as it eroded: dead hosts, HTML instead of JSON, Songlink 429s. It called itself "the most perishable document in the repo" and it was right; that entire layer went with v4.0.0. Kept because it is the evidence base for adopting the engine. |
| [module-engine-runbook.md](module-engine-runbook.md) | ✅ Done — the phased bring-up. Phase 0 (fork, build, prove one track) and Phase 1 (wire into the app) are complete and verified in prod. |
| [override-rework-plan.md](override-rework-plan.md) | ✅ Done — service-selection refonte shipped + verified (phases 1/2a/2b/3). **Still the record of how `ExecuteDownload`'s fallback chain works — the engine plugs into it.** |
| [settings-source-of-truth.md](settings-source-of-truth.md) | ✅ Done — settings made backend-authoritative, single override, verified prod. |
| [upstream-catchup.md](upstream-catchup.md) | ✅ Done for the independent parts (S8, S2). The remainder (S6/S7/S4/S1 = external provider layer) is superseded by the engine. |
| [service-selection-map.md](service-selection-map.md) | 🕰️ Historical — describes the **pre-refonte** code. Superseded by override-rework (done). |
| [audit-refactoring-couche2.md](audit-refactoring-couche2.md) | ✅ Closed audit (R1–R12). |
| [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) | ✅ Closed finding (ffmpeg on `scratch`). Its §4bis (what hardening does NOT cover) is echoed in [deployment-hardening.md](deployment-hardening.md). |
| [deployment-hardening.md](deployment-hardening.md) | 🕰️ Historical — predates the engine entirely: no `spotiflac-engine`, no `shm_size`, no embedded browser, and its compose is not the one running. Kept because it is the only account of **why** the deployment looks the way it does — the topology, the login DoS, the order changes must be applied in. The decisions still in force (`stop_grace_period: 45s`, `/tmp` on disk, `TRUST_PROXY_HEADERS`, and the `mem_limit` the kernel discards) moved to [../deployment.md § Durcissement](../deployment.md#durcissement) on 2026-08-07. |
| [external-api-layer.md](external-api-layer.md) | ⛔ Superseded — the plan to build/verify the community Qobuz/Amazon/DRM layer **ourselves**. The engine takes over that entire layer. |
| [provider-matching-investigation.md](provider-matching-investigation.md) | ⛔ Superseded — the MusicBrainz→Qobuz matching pipeline (Go). The engine does its own resolution/matching, so it is **not implemented**. Its finding that **`GetISRCDirect` already fetches the ISRC from Spotify remains true and is used** by the BYOT-Tidal path. |
