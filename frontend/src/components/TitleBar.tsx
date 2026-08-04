import { LogOut, User } from "lucide-react";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

interface TitleBarProps {
    onLogout?: () => void;
    userName?: string;
}

export function TitleBar({ onLogout, userName }: TitleBarProps) {
    return (
        <>
            <div
                // Was the desktop window's drag region; in a browser it is
                // simply the blurred strip the page scrolls under.
                className="fixed top-0 left-14 right-0 h-10 z-40 bg-background/80 backdrop-blur-sm"
            />
            <div className="fixed top-1.5 right-2 z-50 flex h-7 gap-0.5 items-center">
                <TooltipProvider>
                    {userName && (
                        <Tooltip>
                            <TooltipTrigger asChild>
                                <div
                                    className="flex items-center gap-1.5 px-2 h-7 text-xs text-muted-foreground rounded hover:bg-muted transition-colors cursor-default select-none"
                                >
                                    <User className="w-3 h-3 shrink-0" />
                                    <span className="max-w-[100px] truncate">{userName}</span>
                                </div>
                            </TooltipTrigger>
                            <TooltipContent side="bottom">{userName}</TooltipContent>
                        </Tooltip>
                    )}
                    {onLogout && (
                        <Tooltip>
                            <TooltipTrigger asChild>
                                <button
                                    onClick={onLogout}
                                    className="w-8 h-7 flex items-center justify-center hover:bg-destructive hover:text-white transition-colors rounded"
                                    aria-label="Logout"
                                >
                                    <LogOut className="w-3.5 h-3.5" />
                                </button>
                            </TooltipTrigger>
                            <TooltipContent side="bottom">Logout</TooltipContent>
                        </Tooltip>
                    )}
                </TooltipProvider>
            </div>
        </>
    );
}
