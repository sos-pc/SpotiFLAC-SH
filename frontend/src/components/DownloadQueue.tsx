import { useState, type ReactNode } from "react";
import { X, Download, CheckCircle2, XCircle, Clock, FileCheck, Trash2, HardDrive, Zap, Timer, FileDown, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { ClearCompletedDownloads, ClearAllDownloads, ExportFailedDownloads } from "@/lib/rpc";
import { useDownloadQueueData, type QueueItem } from "@/hooks/useDownloadQueueData";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
interface DownloadQueueProps {
    isOpen: boolean;
    onClose: () => void;
}
export function DownloadQueue({ isOpen, onClose }: DownloadQueueProps) {
    const queueInfo = useDownloadQueueData();
    const handleClearHistory = async () => {
        try {
            await ClearCompletedDownloads();
        }
        catch (error) {
            console.error("Failed to clear history:", error);
        }
    };
    const handleReset = async () => {
        try {
            await ClearAllDownloads();
            toast.success("Download queue reset");
        }
        catch (error) {
            console.error("Failed to reset queue:", error);
        }
    };
    const handleExportFailed = async () => {
        try {
            const message = await ExportFailedDownloads();
            if (message.startsWith("EXPORT:")) {
                const csv = message.slice(7);
                const blob = new Blob([csv], { type: "text/csv" });
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = "failed_downloads.csv";
                a.click();
                URL.revokeObjectURL(url);
                toast.success("Failures exported");
            } else {
                toast.info(message);
            }
        } catch (error) {
            console.error("Failed to export:", error);
            toast.error(`Failed to export: ${error}`);
        }
    };
    const getStatusIcon = (status: string) => {
        switch (status) {
            case "downloading":
                return <Download className="h-4 w-4 text-blue-500 animate-bounce"/>;
            case "completed":
                return <CheckCircle2 className="h-4 w-4 text-green-500"/>;
            case "failed":
                return <XCircle className="h-4 w-4 text-red-500"/>;
            case "skipped":
                return <FileCheck className="h-4 w-4 text-yellow-500"/>;
            case "queued":
                return <Clock className="h-4 w-4 text-muted-foreground"/>;
            default:
                return null;
        }
    };
    const getStatusBadge = (status: string) => {
        const variants: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
            downloading: "default",
            completed: "outline",
            failed: "destructive",
            skipped: "secondary",
            queued: "outline",
        };
        return (<Badge variant={variants[status] || "outline"} className="text-xs">
      {status}
    </Badge>);
    };
    // Takes whole seconds, already computed by useDownloadQueueData. It used to
    // take a start timestamp and subtract Date.now() itself — which is how a
    // fractional epoch turned into "1.0179998874664307s" on screen, since only
    // one side of that subtraction was rounded.
    const formatDuration = (durationSeconds: number) => {
        if (durationSeconds <= 0)
            return "—";
        const hours = Math.floor(durationSeconds / 3600);
        const minutes = Math.floor((durationSeconds % 3600) / 60);
        const seconds = durationSeconds % 60;
        if (hours > 0) {
            return `${hours}h ${minutes}m ${seconds}s`;
        }
        else if (minutes > 0) {
            return `${minutes}m ${seconds}s`;
        }
        else {
            return `${seconds}s`;
        }
    };
    const [filterStatus, setFilterStatus] = useState<string>("all");
    const toggleFilter = (status: string) => {
        setFilterStatus(prev => prev === status ? "all" : status);
    };
    const filteredQueue = queueInfo.queue.filter((item) => {
        if (filterStatus === "all")
            return true;
        return item.status === filterStatus;
    });
    // Collapsed by default. A batch of 2561 tracks is the case this view exists
    // for, and expanding it is a deliberate act rather than the starting state.
    const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set());
    const toggleGroup = (key: string) => {
        setExpandedGroups(prev => {
            const next = new Set(prev);
            if (next.has(key))
                next.delete(key);
            else
                next.add(key);
            return next;
        });
    };
    // The status filters hunt individual tracks across every batch, so a filter
    // shows the flat list it always showed. Grouping is the unfiltered view's
    // job: an overview of what was asked for, not a way to find one track.
    const renderItem = (item: QueueItem) => (<QueueRow key={item.id} item={item} icon={getStatusIcon(item.status)} badge={getStatusBadge(item.status)}/>);
    return (<Dialog open={isOpen} onOpenChange={onClose}>
    <DialogContent className="max-w-[1200px] w-[95vw] max-h-[80vh] flex flex-col p-0 gap-0 [&>button]:hidden">
      <DialogHeader className="px-6 pt-6 pb-4 border-b space-y-0">
        <div className="flex items-center justify-between mb-4">
          <DialogTitle className="text-lg font-semibold hover:text-primary transition-colors cursor-pointer" onClick={handleReset}>Download Queue</DialogTitle>
          <div className="flex items-center gap-2">
            {(queueInfo.completed_count > 0 || queueInfo.failed_count > 0 || queueInfo.skipped_count > 0) && (<Button variant="ghost" size="sm" className="h-7 text-xs gap-1.5" onClick={handleClearHistory}>
              <Trash2 className="h-3 w-3"/>
              Clear History
            </Button>)}
            {queueInfo.failed_count > 0 && (<Button variant="ghost" size="sm" className="h-7 text-xs gap-1.5" onClick={handleExportFailed}>
              <FileDown className="h-3 w-3"/>
              Export Failures
            </Button>)}
            <Button variant="ghost" size="icon" className="h-7 w-7 rounded-full hover:bg-muted" onClick={onClose}>
              <X className="h-4 w-4"/>
            </Button>
          </div>
        </div>


        <div className="flex items-center gap-4 text-sm">
          <div className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === 'queued' ? 'bg-secondary px-2 py-0.5 rounded-md ring-1 ring-border' : ''}`} onClick={() => toggleFilter('queued')}>
            <Clock className="h-3.5 w-3.5 text-muted-foreground"/>
            <span className="text-muted-foreground">Queued:</span>
            <span className="font-semibold">{queueInfo.queued_count}</span>
          </div>
          <div className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === 'completed' ? 'bg-green-500/10 px-2 py-0.5 rounded-md ring-1 ring-green-500/20' : ''}`} onClick={() => toggleFilter('completed')}>
            <CheckCircle2 className="h-3.5 w-3.5 text-green-500"/>
            <span className="text-muted-foreground">Completed:</span>
            <span className="font-semibold">{queueInfo.completed_count}</span>
          </div>
          <div className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === 'skipped' ? 'bg-yellow-500/10 px-2 py-0.5 rounded-md ring-1 ring-yellow-500/20' : ''}`} onClick={() => toggleFilter('skipped')}>
            <FileCheck className="h-3.5 w-3.5 text-yellow-500"/>
            <span className="text-muted-foreground">Skipped:</span>
            <span className="font-semibold">{queueInfo.skipped_count}</span>
          </div>
          <div className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === 'failed' ? 'bg-red-500/10 px-2 py-0.5 rounded-md ring-1 ring-red-500/20' : ''}`} onClick={() => toggleFilter('failed')}>
            <XCircle className="h-3.5 w-3.5 text-red-500"/>
            <span className="text-muted-foreground">Failed:</span>
            <span className="font-semibold">{queueInfo.failed_count}</span>
          </div>
        </div>


        <div className="flex items-center gap-4 text-sm pt-3 mt-3 border-t">
          <div className="flex items-center gap-1.5">
            <HardDrive className="h-3.5 w-3.5 text-muted-foreground"/>
            <span className="text-muted-foreground">Downloaded:</span>
            <span className="font-semibold font-mono">
              {queueInfo.total_downloaded > 0 ? `${queueInfo.total_downloaded.toFixed(2)} MB` : "0.00 MB"}
            </span>
          </div>
          <div className="flex items-center gap-1.5">
            <Zap className="h-3.5 w-3.5 text-muted-foreground"/>
            <span className="text-muted-foreground">Speed:</span>
            <span className="font-semibold font-mono">
              {queueInfo.current_speed > 0 && queueInfo.is_downloading
            ? `${queueInfo.current_speed.toFixed(2)} MB/s`
            : "—"}
            </span>
          </div>
          <div className="flex items-center gap-1.5">
            <Timer className="h-3.5 w-3.5 text-muted-foreground"/>
            <span className="text-muted-foreground">Duration:</span>
            <span className="font-semibold font-mono">
              {formatDuration(queueInfo.duration_seconds)}
            </span>
          </div>
        </div>

      </DialogHeader>


      <div className="flex-1 overflow-y-auto px-6 custom-scrollbar">
        <div className="space-y-2 py-4">
          {queueInfo.queue.length === 0 ? (<div className="text-center py-12 text-muted-foreground">
            <Download className="h-12 w-12 mx-auto mb-3 opacity-20"/>
            <p>No downloads in queue</p>
          </div>) : filteredQueue.length === 0 ? (<div className="text-center py-12 text-muted-foreground">
             <p>No downloads with status "{filterStatus}"</p>
             <Button variant="link" onClick={() => setFilterStatus("all")}>Clear filter</Button>
            </div>) : filterStatus !== "all" ? (filteredQueue.map(renderItem)) : (queueInfo.groups.map((group) => group.total === 1 ? (
            // A single track downloaded from the search bar is not a batch.
            // Wrapping it in collapsible chrome would invent a grouping the
            // user never made.
            renderItem(group.items[0])
          ) : (<div key={group.key} className="border rounded-lg overflow-hidden">
            <button
              type="button"
              onClick={() => toggleGroup(group.key)}
              className="w-full flex items-center gap-3 p-3 text-left hover:bg-muted/30 transition-colors"
            >
              <ChevronRight className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${expandedGroups.has(group.key) ? "rotate-90" : ""}`}/>
              <div className="flex-1 min-w-0">
                <p className="font-medium truncate">{group.label}</p>
                <p className="text-xs text-muted-foreground font-mono">
                  {group.completed + group.skipped}/{group.total} done
                  {group.failed > 0 && <span className="text-red-500"> · {group.failed} failed</span>}
                  {group.downloading > 0 && <span className="text-blue-500"> · downloading</span>}
                  {group.sizeMB > 0 && ` · ${group.sizeMB.toFixed(2)} MB`}
                </p>
              </div>
              {/* Same denominator as the line above, deliberately different
                  numerator: the text counts what succeeded, the bar counts what
                  is settled — failures included, which is why it turns red
                  rather than stalling short of the end on a batch that is over. */}
              <div className="w-24 shrink-0 h-1.5 rounded-full bg-muted overflow-hidden" aria-hidden="true">
                <div
                  className={group.failed > 0 ? "h-full bg-red-500/60" : "h-full bg-green-500/60"}
                  style={{ width: `${Math.min(100, Math.round(((group.completed + group.skipped + group.failed) / group.total) * 100))}%` }}
                />
              </div>
            </button>
            {expandedGroups.has(group.key) && (<div className="border-t p-2 space-y-2 bg-muted/10">
              {group.items.map(renderItem)}
            </div>)}
          </div>)))}
        </div>
      </div>
    </DialogContent>
  </Dialog>);
}

// One row. Extracted so the grouped view and the filtered flat view render an
// item identically — two copies of this would drift the moment either changed.
function QueueRow({ item, icon, badge }: { item: QueueItem; icon: ReactNode; badge: ReactNode }) {
    return (<div className="border rounded-lg p-3 hover:bg-muted/30 transition-colors">
      <div className="flex items-start gap-3">
        <div className="mt-1">{icon}</div>

        <div className="flex-1 min-w-0">
          <div className="flex items-start justify-between gap-2 mb-1">
            <div className="flex-1 min-w-0">
              <p className="font-medium truncate">{item.track_name}</p>
              <p className="text-sm text-muted-foreground truncate">
                {item.artist_name}
                {item.album_name && ` • ${item.album_name}`}
              </p>
            </div>
            {badge}
          </div>

          {/* The row's own numbers only. This used to fall back to the queue's
              global is_downloading / current_speed when the item reported
              nothing — but current_speed is the max across downloading jobs and
              there is a single worker, so the "other" job it borrowed from was
              always this one. The fallback restated the same value by a longer
              route, and would have shown a different track's speed the day that
              stopped being true. */}
          {item.status === "downloading" && (<div className="flex items-center gap-3 mt-1.5 text-xs text-muted-foreground font-mono">
            <span>{item.total_size > 0 ? `${item.total_size.toFixed(2)} MB` : "Starting…"}</span>
            <span>{item.speed > 0 ? `${item.speed.toFixed(2)} MB/s` : "—"}</span>
          </div>)}

          {item.status === "completed" && (<div className="flex items-center gap-3 mt-1.5 text-xs text-muted-foreground">
            <span className="font-mono">{item.total_size > 0 ? `${item.total_size.toFixed(2)} MB` : "—"}</span>
          </div>)}

          {item.status === "skipped" && (<div className="mt-1.5 text-xs text-muted-foreground">
            File already exists
          </div>)}

          {item.status === "failed" && item.error_message && (<div className="mt-1.5 text-xs text-red-500 bg-red-50 dark:bg-red-950/20 rounded px-2 py-1">
            {item.error_message}
          </div>)}

          {(item.status === "completed" || item.status === "skipped") && item.file_path && (<div className="mt-1.5 text-xs text-muted-foreground truncate font-mono">
            {item.file_path}
          </div>)}
        </div>
      </div>
    </div>);
}
