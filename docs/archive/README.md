# Archive

Docs that are **no longer live guidance**. Two reasons a doc lands here:

- **✅ Done** — the work shipped and was verified in prod; kept only to understand *why* the
  code is the way it is.
- **⛔ Superseded by the download-engine project** — the approach it planned was replaced by
  delegating downloads to the forked engine (see [../module-version-integration.md](../module-version-integration.md)).

Nothing here describes the current or intended state of the code. For that, start at
[../README.md](../README.md).

| Doc | Why archived |
|---|---|
| [override-rework-plan.md](override-rework-plan.md) | ✅ Done — service-selection refonte shipped + verified (phases 1/2a/2b/3). **Still the record of how `ExecuteDownload`'s fallback chain works — the engine plugs into it.** |
| [settings-source-of-truth.md](settings-source-of-truth.md) | ✅ Done — settings made backend-authoritative, single override, verified prod. |
| [upstream-catchup.md](upstream-catchup.md) | ✅ Done for the independent parts (S8, S2). The remainder (S6/S7/S4/S1 = external provider layer) is superseded by the engine. |
| [service-selection-map.md](service-selection-map.md) | 🕰️ Historical — describes the **pre-refonte** code. Superseded by override-rework (done). |
| [audit-refactoring-couche2.md](audit-refactoring-couche2.md) | ✅ Closed audit (R1–R12). |
| [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) | ✅ Closed finding (ffmpeg on `scratch`). Its §4bis (what hardening does NOT cover) is echoed in the active `deployment-hardening.md`. |
| [external-api-layer.md](external-api-layer.md) | ⛔ Superseded — the plan to build/verify the community Qobuz/Amazon/DRM layer **ourselves**. The engine takes over that entire layer. |
| [provider-matching-investigation.md](provider-matching-investigation.md) | ⛔ Superseded — the MusicBrainz→Qobuz matching pipeline (Go). The engine does its own resolution/matching, so it is **not implemented**. Its finding that **`GetISRCDirect` already fetches the ISRC from Spotify remains true and is used** by the BYOT-Tidal path. |
