import { useState } from "react";
import { useJobsStreamEvent } from "./useJobsStreamEvent";

export interface Job {
  id: string;
  status: "pending" | "downloading" | "done" | "failed" | "skipped";
  track_name: string;
  artist_name: string;
  album_name?: string;
  total_size: number;
  file_path?: string;
  batch_id?: string;
  error?: string;
  progress: number;
  speed?: number;
  started_at?: string;
  // Set on every status transition, so for a job that is no longer running it
  // is when it stopped. Already on the wire — the SSE payload is the whole
  // server-side job — and only declared here now that something reads it.
  updated_at?: string;
  // What the user asked for when this job was created: the playlist a
  // watchlist sync is filling, or the album a search-bar download expanded.
  // On the wire since jobs existed; nothing read it until the queue started
  // grouping by batch.
  playlist_name?: string;
}

// The panel's own vocabulary, not the server's: Job.status says pending/done,
// these say queued/completed. Named rather than written inline because the view
// keys three exhaustive records off it — icon, badge variant, label — so adding
// a state here is a compile error in every place that has to say something
// about it, instead of a row that silently renders blank.
export type QueueStatus =
  | "queued"
  | "downloading"
  | "completed"
  | "failed"
  | "skipped";

export interface QueueItem extends Omit<Job, "status" | "speed"> {
  status: QueueStatus;
  error_message: string;
  speed: number;
}

// A batch, or a single download that belongs to none.
//
// The panel showed one row per track, which is the wrong unit for this app: a
// watchlist sync enqueues one job per track, and the reference deployment has a
// playlist of 2561. The user performed *one* action and got 2561 unrelated
// lines, none of which answered "how far along is it". Every job already
// carries batch_id and playlist_name — the grouping needed no server change,
// only somewhere to put it.
export interface QueueGroup {
  key: string;
  label: string;
  items: QueueItem[];
  total: number;
  completed: number;
  failed: number;
  skipped: number;
  queued: number;
  downloading: number;
  sizeMB: number;
  // True while anything in the group is still queued or running. Drives
  // ordering, so the batch being worked on stays at the top.
  active: boolean;
  // Most recent updated_at in the group, ms. Ordering key for finished groups.
  lastActivity: number;
}

// groupQueue turns a flat item list into batches, newest activity first.
//
// A job with no batch_id is its own group of one: a single track downloaded
// from the search bar is not a batch, and folding those into a pseudo-batch
// would invent a grouping the user never made.
//
// Pure on purpose. There is no test runner in this frontend — only `tsc -b` and
// eslint — so the logic that can be reasoned about in isolation is kept out of
// the component, where it would be reachable only by clicking.
export function groupQueue(items: QueueItem[]): QueueGroup[] {
  const byKey = new Map<string, QueueItem[]>();
  for (const item of items) {
    const key = item.batch_id || `single:${item.id}`;
    const bucket = byKey.get(key);
    if (bucket) bucket.push(item);
    else byKey.set(key, [item]);
  }

  const groups: QueueGroup[] = [];
  for (const [key, groupItems] of byKey) {
    const count = (s: QueueItem["status"]) =>
      groupItems.filter((i) => i.status === s).length;

    const named = groupItems.find((i) => i.playlist_name)?.playlist_name;
    const album = groupItems.find((i) => i.album_name)?.album_name;
    const single = groupItems.length === 1 ? groupItems[0].track_name : "";

    groups.push({
      key,
      // Falling back through what the user would recognise, ending at the
      // track itself rather than at a batch id nobody has ever seen.
      label: named || album || single || `Batch ${key.slice(0, 8)}`,
      items: groupItems,
      total: groupItems.length,
      completed: count("completed"),
      failed: count("failed"),
      skipped: count("skipped"),
      queued: count("queued"),
      downloading: count("downloading"),
      sizeMB: groupItems.reduce((s, i) => s + (i.total_size || 0), 0),
      active: groupItems.some(
        (i) => i.status === "downloading" || i.status === "queued",
      ),
      lastActivity: groupItems.reduce((max, i) => {
        const t = i.updated_at ? new Date(i.updated_at).getTime() : NaN;
        return Number.isFinite(t) && t > max ? t : max;
      }, 0),
    });
  }

  // What is being worked on now, first; then the most recently finished.
  return groups.sort((a, b) => {
    if (a.active !== b.active) return a.active ? -1 : 1;
    return b.lastActivity - a.lastActivity;
  });
}

// Map Job status/fields to the shape the existing UI expects
function toQueueItem(job: Job): QueueItem {
  return {
    ...job,
    status:
      job.status === "pending"
        ? "queued"
        : job.status === "done"
          ? "completed"
          : job.status,
    error_message: job.error ?? "",
    speed: job.speed ?? 0,
  };
}

export function useDownloadQueueData() {
  const [jobs, setJobs] = useState<Map<string, Job>>(new Map());

  useJobsStreamEvent("job_update", (e: MessageEvent) => {
    const job: Job = JSON.parse(e.data);
    setJobs((prev) => {
      const next = new Map(prev);
      next.set(job.id, job);
      return next;
    });
  });

  useJobsStreamEvent("job_deleted", (e: MessageEvent) => {
    const { id } = JSON.parse(e.data) as { id: string };
    setJobs((prev) => {
      const next = new Map(prev);
      next.delete(id);
      return next;
    });
  });

  const jobsArray = Array.from(jobs.values());
  const queue = jobsArray.map(toQueueItem);

  return {
    is_downloading: jobsArray.some((j) => j.status === "downloading"),
    queue,
    // The same items, batched. Kept alongside `queue` rather than replacing it:
    // the status filters still hunt individual tracks across every batch, and
    // that view has no use for the grouping.
    groups: groupQueue(queue),
    current_speed: jobsArray
      .filter((j) => j.status === "downloading")
      .reduce((max, j) => Math.max(max, j.speed ?? 0), 0),
    total_downloaded: jobsArray
      .filter((j) => j.status === "done")
      .reduce((s, j) => s + (j.total_size || 0), 0),
    // How long the work shown here has taken: from the first job that started
    // to the last one that stopped, or to now while any is still running.
    //
    // It replaces session_start_time, which was neither a session nor usable as
    // one. That took the oldest started_at among jobs *currently downloading or
    // pending*, so it restarted at zero on every new download and went blank the
    // moment the queue drained — while Downloaded and Completed beside it stayed
    // cumulative over the whole visible history. A panel reading
    // "Downloaded: 124 MB · Duration: 1s" was showing the age of the one job
    // that happened to be running.
    //
    // It also handed the view an epoch in fractional seconds
    // (getTime() / 1000), which the view subtracted from a floored now and
    // printed raw: "Duration: 1.0179998874664307s". Whole seconds are computed
    // here instead, so the view only formats.
    duration_seconds: (() => {
      const startedAt = jobsArray
        .map((j) => (j.started_at ? new Date(j.started_at).getTime() : NaN))
        .filter((t) => Number.isFinite(t) && t > 0);
      if (startedAt.length === 0) return 0;
      const start = Math.min(...startedAt);

      const stillRunning = jobsArray.some(
        (j) => j.status === "downloading" || j.status === "pending",
      );
      let end: number;
      if (stillRunning) {
        // A label a few seconds stale between renders is imperceptible; not
        // worth a ticking clock just to satisfy render-purity analysis.
        // eslint-disable-next-line react-hooks/purity
        end = Date.now();
      } else {
        const stoppedAt = jobsArray
          .map((j) => (j.updated_at ? new Date(j.updated_at).getTime() : NaN))
          .filter((t) => Number.isFinite(t) && t > 0);
        end = stoppedAt.length > 0 ? Math.max(...stoppedAt) : start;
      }
      return Math.max(0, Math.floor((end - start) / 1000));
    })(),
    queued_count: jobsArray.filter((j) => j.status === "pending").length,
    completed_count: jobsArray.filter((j) => j.status === "done").length,
    failed_count: jobsArray.filter((j) => j.status === "failed").length,
    skipped_count: jobsArray.filter((j) => j.status === "skipped").length,
  };
}
