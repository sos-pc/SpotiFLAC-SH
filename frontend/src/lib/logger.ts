export type LogLevel = "info" | "success" | "warning" | "error" | "debug";
export type LogSource = "web" | "docker";
export interface LogEntry {
    timestamp: Date;
    level: LogLevel;
    message: string;
    source: LogSource;
}
class Logger {
    private logs: LogEntry[] = [];
    private maxLogs = 1000;
    private listeners: Set<() => void> = new Set();
    private addLog(level: LogLevel, message: string, source: LogSource = "web") {
        const entry: LogEntry = {
            timestamp: new Date(),
            level,
            message: message,
            source,
        };
        this.logs.push(entry);
        if (this.logs.length > this.maxLogs) {
            this.logs.shift();
        }
        this.notifyListeners();
    }
    info(message: string) {
        this.addLog("info", message);
    }
    success(message: string) {
        this.addLog("success", message);
    }
    warning(message: string) {
        this.addLog("warning", message);
    }
    error(message: string) {
        this.addLog("error", message);
    }
    debug(message: string) {
        this.addLog("debug", message);
    }
    // ingestBackend records a backend (docker) log line captured by the
    // server's stdout tee — see the server_log SSE event and
    // GET /api/v1/admin/logs (logbuffer.go on the backend).
    ingestBackend(time: string, level: string, message: string) {
        const mapped: LogLevel =
            level === "error" ? "error" : level === "warning" ? "warning" : "info";
        this.logs.push({
            timestamp: new Date(time),
            level: mapped,
            message,
            source: "docker",
        });
        if (this.logs.length > this.maxLogs) {
            this.logs.shift();
        }
        this.notifyListeners();
    }
    getLogs(): LogEntry[] {
        return [...this.logs];
    }
    clear() {
        this.logs = [];
        this.notifyListeners();
    }
    subscribe(listener: () => void) {
        this.listeners.add(listener);
        return () => this.listeners.delete(listener);
    }
    private notifyListeners() {
        this.listeners.forEach((listener) => listener());
    }
}
export const logger = new Logger();
