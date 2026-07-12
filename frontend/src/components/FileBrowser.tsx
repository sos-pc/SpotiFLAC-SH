import { useState, useEffect, useCallback } from "react";
import { FolderOpen, ChevronRight, Home, ArrowLeft, Check, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ListDirectoryFiles, GetUserHomeDir } from "@/lib/rpc";

interface FileInfo {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  children?: FileInfo[];
}

interface FileBrowserProps {
  isOpen: boolean;
  onClose: () => void;
  onSelect: (path: string) => void;
  initialPath?: string;
  title?: string;
}

export function FileBrowser({ isOpen, onClose, onSelect, initialPath, title = "Select Folder" }: FileBrowserProps) {
  const [currentPath, setCurrentPath] = useState<string>("");
  const [entries, setEntries] = useState<FileInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<string[]>([]);

  const navigate = useCallback(async (path: string) => {
    setLoading(true);
    setError(null);
    try {
      const result = await ListDirectoryFiles(path);
      const dirs = (result || []).filter((f: FileInfo) => f.is_dir && !f.name.startsWith("."));
      setEntries(dirs);
      setCurrentPath(path);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to read directory");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    const init = async () => {
      let startPath = initialPath || "";
      if (!startPath) {
        try { startPath = await GetUserHomeDir(); } catch { startPath = "/home/nonroot"; }
      }
      setHistory([]);
      navigate(startPath);
    };
    init();
  }, [isOpen, initialPath, navigate]);

  const handleEnter = (dir: FileInfo) => {
    setHistory(h => [...h, currentPath]);
    navigate(dir.path);
  };

  const handleBack = () => {
    if (history.length === 0) return;
    const prev = history[history.length - 1];
    setHistory(h => h.slice(0, -1));
    navigate(prev);
  };

  const handleHome = async () => {
    try {
      const home = await GetUserHomeDir();
      setHistory(h => [...h, currentPath]);
      navigate(home);
    } catch (err) {
      console.error("Failed to resolve home directory:", err);
    }
  };

  const handleSelect = () => {
    onSelect(currentPath);
    onClose();
  };

  // Breadcrumb parts
  const parts = currentPath.split("/").filter(Boolean);

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>

        {/* Toolbar */}
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" onClick={handleBack} disabled={history.length === 0}>
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <Button variant="ghost" size="icon" onClick={handleHome}>
            <Home className="h-4 w-4" />
          </Button>
          {/* Breadcrumb */}
          <div className="flex items-center gap-1 text-sm text-muted-foreground overflow-hidden flex-1 min-w-0">
            <span>/</span>
            {parts.map((part, i) => (
              <span key={i} className="flex items-center gap-1 min-w-0">
                <button
                  className="hover:text-foreground truncate max-w-[100px]"
                  onClick={() => {
                    const target = "/" + parts.slice(0, i + 1).join("/");
                    setHistory(h => [...h, currentPath]);
                    navigate(target);
                  }}
                >
                  {part}
                </button>
                {i < parts.length - 1 && <ChevronRight className="h-3 w-3 shrink-0" />}
              </span>
            ))}
          </div>
        </div>

        {/* Current path */}
        <div className="text-xs text-muted-foreground bg-muted px-3 py-1.5 rounded font-mono truncate">
          {currentPath}
        </div>

        {/* Directory listing */}
        <div className="border rounded-md overflow-y-auto h-64">
          {loading && (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              Loading...
            </div>
          )}
          {error && (
            <div className="flex items-center justify-center h-full text-sm text-destructive px-4 text-center">
              {error}
            </div>
          )}
          {!loading && !error && entries.length === 0 && (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              No subfolders
            </div>
          )}
          {!loading && !error && entries.map((entry) => (
            <button
              key={entry.path}
              className="w-full flex items-center gap-3 px-4 py-2.5 text-sm hover:bg-accent text-left transition-colors"
              onDoubleClick={() => handleEnter(entry)}
              onClick={() => {
                setHistory(h => [...h, currentPath]);
                navigate(entry.path);
              }}
            >
              <FolderOpen className="h-4 w-4 text-yellow-500 shrink-0" />
              <span className="truncate">{entry.name}</span>
            </button>
          ))}
        </div>

        {/* Actions */}
        <div className="flex justify-between items-center pt-1">
          <p className="text-xs text-muted-foreground">Double-click or click to navigate</p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose}>
              <X className="h-4 w-4 mr-1" /> Cancel
            </Button>
            <Button size="sm" onClick={handleSelect}>
              <Check className="h-4 w-4 mr-1" /> Select
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
