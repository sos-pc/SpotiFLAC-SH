import { getStreamToken } from "./auth";

// jobsStream is a single shared connection to the backend's multiplexed SSE
// endpoint (/api/v1/jobs/stream — see sse.go), which already fans
// job_update/job_deleted/queue_cleared/watchlist_synced/watchlist_repaired/
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
  | "queue_cleared"
  | "watchlist_synced"
  | "watchlist_repaired"
  | "server_log";

type Listener = (e: MessageEvent) => void;

const RECONNECT_DELAY_MS = 3000;

class JobsStreamHub {
  private es: EventSource | null = null;
  private connecting = false;
  private listeners = new Map<JobsStreamEventType, Set<Listener>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  private totalListenerCount(): number {
    let n = 0;
    for (const set of this.listeners.values()) n += set.size;
    return n;
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
      const token = await getStreamToken();
      // A subscriber may have unsubscribed while this awaited the token.
      if (!token || this.totalListenerCount() === 0) return;

      const es = new EventSource(
        `/api/v1/jobs/stream?token=${encodeURIComponent(token)}`,
      );
      for (const type of this.listeners.keys()) {
        this.attachType(es, type);
      }
      es.onerror = () => {
        es.close();
        if (this.es === es) this.es = null;
        if (this.totalListenerCount() === 0) return;
        this.reconnectTimer = setTimeout(
          () => this.connect(),
          RECONNECT_DELAY_MS,
        );
      };
      this.es = es;
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
      }
    };
  }
}

export const jobsStream = new JobsStreamHub();
