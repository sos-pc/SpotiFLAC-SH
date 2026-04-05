import { useState } from "react";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { HardDrive, Monitor } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

export function DownloadModeToggle() {
    const [mode, setMode] = useState<"server" | "browser">(() =>
        (localStorage.getItem("download_mode") as "server" | "browser") || "server"
    );

    const handleChange = (value: string) => {
        if (value !== "server" && value !== "browser") return;
        localStorage.setItem("download_mode", value);
        setMode(value);
        window.dispatchEvent(new CustomEvent("spotif:downloadModeChange", { detail: value }));
    };

    return (
        <Tooltip>
            <TooltipTrigger asChild>
                <ToggleGroup
                    type="single"
                    value={mode}
                    onValueChange={handleChange}
                    variant="outline"
                    size="sm"
                    className="h-9"
                >
                    <ToggleGroupItem value="server" aria-label="Save to server">
                        <HardDrive className="h-4 w-4" />
                    </ToggleGroupItem>
                    <ToggleGroupItem value="browser" aria-label="Download to browser">
                        <Monitor className="h-4 w-4" />
                    </ToggleGroupItem>
                </ToggleGroup>
            </TooltipTrigger>
            <TooltipContent>
                <p>{mode === "server" ? "Save to server folder" : "Download to your device"}</p>
            </TooltipContent>
        </Tooltip>
    );
}
