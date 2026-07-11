import { useEffect, useRef } from "react";
import { jobsStream, type JobsStreamEventType } from "@/lib/jobsStream";

// Subscribes to one event type on the shared jobs SSE connection (see
// lib/jobsStream.ts) for the lifetime of the calling component. The
// listener is invoked through a ref, so callers can pass a fresh closure on
// every render without needing to memoize it or risking a stale closure —
// only mount/unmount and changes to `type`/`enabled` re-subscribe.
export function useJobsStreamEvent(
  type: JobsStreamEventType,
  listener: (e: MessageEvent) => void,
  enabled: boolean = true,
) {
  const listenerRef = useRef(listener);
  useEffect(() => {
    listenerRef.current = listener;
  });

  useEffect(() => {
    if (!enabled) return;
    return jobsStream.subscribe(type, (e) => listenerRef.current(e));
  }, [type, enabled]);
}
