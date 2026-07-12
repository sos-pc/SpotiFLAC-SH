import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import { Download, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
interface DownloadProgressToastProps {
  onClick: () => void;
}
export function DownloadProgressToast({ onClick }: DownloadProgressToastProps) {
  const queueInfo = useDownloadQueueData();
  const downloadingItems = queueInfo.queue.filter(
    (item) => item.status === "downloading",
  );
  const hasActiveDownloads = queueInfo.queue.some(
    (item) => item.status === "queued" || item.status === "downloading",
  );
  const totalMB = downloadingItems.reduce(
    (sum, item) => sum + (item.total_size || 0),
    0,
  );
  const speed = queueInfo.current_speed || 0;
  if (!hasActiveDownloads) {
    return null;
  }
  return (
    <div className="fixed bottom-4 left-[calc(56px+1rem)] z-50 animate-in slide-in-from-bottom-5 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom-5">
      <Button
        variant="outline"
        className="bg-background border rounded-lg shadow-lg p-3 h-auto hover:bg-muted/50 transition-colors cursor-pointer"
        onClick={onClick}
      >
        <div className="flex items-center gap-3">
          <Download
            className={`h-4 w-4 text-primary ${queueInfo.is_downloading ? "animate-bounce" : ""}`}
          />
          <div className="flex flex-col min-w-[80px]">
            <p className="text-sm font-medium font-mono tabular-nums">
              {totalMB.toFixed(2)} MB
            </p>
            {speed > 0 && (
              <p className="text-xs text-muted-foreground font-mono tabular-nums">
                {speed.toFixed(2)} MB/s
              </p>
            )}
          </div>
          <ChevronRight className="h-4 w-4 text-muted-foreground ml-1" />
        </div>
      </Button>
    </div>
  );
}
