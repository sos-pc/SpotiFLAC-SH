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
}

export interface QueueItem extends Omit<Job, "status" | "speed"> {
  status: "queued" | "downloading" | "completed" | "failed" | "skipped";
  error_message: string;
  speed: number;
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
