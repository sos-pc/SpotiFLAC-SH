import { useState } from "react";
import { useJobsStreamEvent } from "./useJobsStreamEvent";

interface Job {
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
}

// Map Job status/fields to the shape the existing UI expects
function toQueueItem(job: Job) {
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

  useJobsStreamEvent("queue_cleared", () => {
    setJobs((prev) => {
      const next = new Map(prev);
      // Supprimer tous les jobs terminaux (done/skipped/failed)
      // Garder pending et downloading (ils sont toujours actifs)
      for (const [id, job] of next) {
        if (job.status !== "pending" && job.status !== "downloading") {
          next.delete(id);
        }
      }
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
    session_start_time: (() => {
      const downloadingJobs = jobsArray.filter(
        (j) => j.status === "downloading" || j.status === "pending",
      );
      if (downloadingJobs.length === 0) return 0;
      const oldest = downloadingJobs
        .filter((j) => j.started_at)
        .map((j) => new Date(j.started_at!).getTime() / 1000)
        .filter((t) => t > 0);
      return oldest.length > 0 ? Math.min(...oldest) : 0;
    })(),
    queued_count: jobsArray.filter((j) => j.status === "pending").length,
    completed_count: jobsArray.filter((j) => j.status === "done").length,
    failed_count: jobsArray.filter((j) => j.status === "failed").length,
    skipped_count: jobsArray.filter((j) => j.status === "skipped").length,
  };
}
