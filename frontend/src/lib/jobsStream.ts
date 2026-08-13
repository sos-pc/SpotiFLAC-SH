import { getStreamToken } from "./auth";

// jobsStream is a single shared connection to the backend's multiplexed SSE
// endpoint (/api/v1/jobs/stream — see sse.go), which already fans
// job_update/job_deleted/watchlist_synced/watchlist_repaired/
// server_log events out to every connected client server-side, per
// authenticated user. Before this, several independent frontend call sites
// (the download queue widgets, the browser-download-mode listener, the
// watchlist page, the debug logger page) each opened their OWN EventSource
// to that exact same endpoint just to filter a different event type
// client-side — up to 5 redundant, identical connections open at once per
// tab, each counting against the browser's per-origin connection limit and
// each holding its own server-side goroutine + channel subscription (see
// SSEHub.subscribe in sse.go). This hub opens ONE connection instead,
// ref-counted across every subscriber, and re-establishes it (with a fresh
// short-lived stream token — see streamTokenTTL in auth.go) on error.
//
// Note: the server sends a one-time snapshot of recent job_update events
// right after a connection opens (see v1JobsStream in sse.go). Only
// subscribers registered before that snapshot arrives will see it — in
// practice this is fine because job_update's only two consumers
// (useDownloadQueueData's callers) always mount together in the same React
// commit, so both register before the connection's async token fetch
// resolves. A subscriber that joins a long-lived connection later (e.g.
// navigating to the watchlist page) only ever needs live events going
// forward, not a historical replay.

export type JobsStreamEventType =
  | "job_update"
  | "job_deleted"
  | "watchlist_synced"
  | "watchlist_repaired"
  | "library_rebuild_done"
  | "retag_incomplete_metadata_done"
  | "server_log";

type Listener = (e: MessageEvent) => void;

// Backoff rather than a flat delay: a server that is down stays down for
// minutes, and a tab asking every three seconds for an hour is a hot loop
// against something that cannot answer. Capped so a long outage still
// reconnects promptly once it ends.
const RECONNECT_BASE_MS = 3000;
const RECONNECT_MAX_MS = 60000;

class JobsStreamHub {
  private es: EventSource | null = null;
  private connecting = false;
  private listeners = new Map<JobsStreamEventType, Set<Listener>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;

  private totalListenerCount(): number {
    let n = 0;
    for (const set of this.listeners.values()) n += set.size;
    return n;
  }

  // The single retry path. Every way a connection can fail to establish routes
  // through here, because the one that did not is how the stream used to die:
  // connect() returned without scheduling anything when the token came back
  // empty, leaving es null, no timer pending, and every subscriber still
  // registered. The panel stopped updating for the life of the tab, silently,
  // over one transient 401.
  private scheduleReconnect() {
    if (this.reconnectTimer || this.totalListenerCount() === 0) return;
    const delay = Math.min(
      RECONNECT_BASE_MS * 2 ** this.reconnectAttempts,
      RECONNECT_MAX_MS,
    );
    this.reconnectAttempts++;
    this.reconnectTimer = setTimeout(() => {
      // Cleared before reconnecting, not after: subscribe() treats a non-null
      // timer as "a reconnect is already pending" and would decline to start
      // one. Leaving a fired timer's id in place made that check read true
      // forever.
      this.reconnectTimer = null;
      void this.connect();
    }, delay);
  }

  private attachType(es: EventSource, type: JobsStreamEventType) {
    es.addEventListener(type, (e: MessageEvent) => {
      this.listeners.get(type)?.forEach((l) => l(e));
    });
  }

  private async connect() {
    if (this.es || this.connecting) return;
    this.connecting = true;
    try {
      let token: string | null | undefined;
      try {
        token = await getStreamToken();
      } catch {
        // Thrown, rather than returning empty. Same outcome for us, and it
        // used to escape connect() entirely as an unhandled rejection from
        // the `void this.connect()` call site.
        token = null;
      }

      // A subscriber may have unsubscribed while this awaited the token. That
      // is the one case with nothing to retry — there is no longer anyone to
      // deliver to.
      if (this.totalListenerCount() === 0) return;
      if (!token) {
        this.scheduleReconnect();
        return;
      }

      const es = new EventSource(
        `/api/v1/jobs/stream?token=${encodeURIComponent(token)}`,
      );
      for (const type of this.listeners.keys()) {
        this.attachType(es, type);
      }
      // Reset on a connection that actually opened, not on one that was merely
      // constructed: `new EventSource` succeeds long before the server answers,
      // so resetting there would clear the backoff on every failed attempt and
      // turn it back into a flat retry.
      es.onopen = () => {
        this.reconnectAttempts = 0;
      };
      es.onerror = () => {
        es.close();
        if (this.es === es) this.es = null;
        this.scheduleReconnect();
      };
      this.es = es;
    } catch {
      // Anything else that throws on the way to a connection — the EventSource
      // constructor on a URL it dislikes, most plausibly. The claim above is
      // that every failure to establish routes through scheduleReconnect, and
      // a bare `finally` would have let this one escape as an unhandled
      // rejection instead, which is the exact shape of the bug being fixed.
      this.scheduleReconnect();
    } finally {
      this.connecting = false;
    }
  }

  subscribe(type: JobsStreamEventType, listener: Listener): () => void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
      if (this.es) this.attachType(this.es, type);
    }
    const activeSet = set;
    activeSet.add(listener);

    if (!this.es && !this.reconnectTimer) {
      void this.connect();
    }

    return () => {
      activeSet.delete(listener);
      if (this.totalListenerCount() === 0) {
        if (this.reconnectTimer) {
          clearTimeout(this.reconnectTimer);
          this.reconnectTimer = null;
        }
        this.es?.close();
        this.es = null;
        this.listeners.clear();
        // The next subscriber is a fresh start, not a continuation of whatever
        // outage emptied this hub. Without the reset it would inherit a
        // minute-long backoff from a page it has nothing to do with.
        this.reconnectAttempts = 0;
      }
    };
  }
}

export const jobsStream = new JobsStreamHub();
