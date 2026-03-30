import { useState, useEffect, useCallback } from "react";
import { flushSync } from "react-dom";
import { Button } from "@/components/ui/button";
import { InputWithContext } from "@/components/ui/input-with-context";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue, } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger, } from "@/components/ui/tooltip";
import { FolderOpen, Save, RotateCcw, Info, ArrowRight, Settings, FolderCog, Key, Link, Copy, Check, Trash2, RefreshCw, ExternalLink, } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { FileBrowser } from "@/components/FileBrowser";
import { getSettings, loadSettings, getSettingsWithDefaults, saveSettings, resetToDefaultSettings, applyThemeMode, applyFont, FONT_OPTIONS, FOLDER_PRESETS, FILENAME_PRESETS, TEMPLATE_VARIABLES, type Settings as SettingsType, type FontFamily, type FolderPreset, type FilenamePreset, } from "@/lib/settings";
import { themes, applyTheme } from "@/lib/themes";

import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { ListAPIKeys, CreateAPIKey, DeleteAPIKey, GetTidalAuthURL, SubmitTidalCallback, GetTidalStatus, DisconnectTidal, GetAPIStatuses, GetAPIProxies, UpdateAPIProxies, type APIKeyMeta, type CreatedAPIKey, type TidalStatus, type ServiceStatus, type ProxyConfig } from "@/lib/rpc";
const TidalIcon = ({ className }: {
    className?: string;
}) => (<svg viewBox="0 0 24 24" className={`inline-block w-[1.1em] h-[1.1em] mr-2 ${className || "fill-muted-foreground"}`}>
    <path d="M4.022 4.5 0 8.516l3.997 3.99 3.997-3.984L4.022 4.5Zm7.956 0L7.994 8.522l4.003 3.984L16 8.484 11.978 4.5Zm8.007 0L24 8.528l-4.003 3.978L16 8.484 19.985 4.5Z"></path>
    <path d="m8.012 16.534 3.991 3.966L16 16.49l-4.003-3.984-3.985 4.028Z"></path>
  </svg>);
const QobuzIcon = ({ className }: {
    className?: string;
}) => (<svg viewBox="0 0 24 24" className={`inline-block w-[1.1em] h-[1.1em] mr-2 ${className || "fill-muted-foreground"}`}>
    <path d="M21.744 9.815C19.836 1.261 8.393-1 3.55 6.64-.618 13.214 4 22 11.988 22c2.387 0 4.63-.83 6.394-2.304l2.252 2.252 1.224-1.224-2.252-2.253c1.983-2.407 2.823-5.586 2.138-8.656Zm-3.508 7.297L16.4 15.275c-.786-.787-2.017.432-1.224 1.225L17 18.326C10.29 23.656.5 16 5.16 7.667c3.502-6.264 13.172-4.348 14.707 2.574.529 2.385-.06 4.987-1.63 6.87Z"></path>
    <path d="M13.4 8.684a3.59 3.59 0 0 0-4.712 1.9 3.59 3.59 0 0 0 1.9 4.712 3.594 3.594 0 0 0 4.711-1.89 3.598 3.598 0 0 0-1.9-4.722Zm-.737 3.591a.727.727 0 0 1-.965.384.727.727 0 0 1-.384-.965.727.727 0 0 1 .965-.384.73.73 0 0 1 .384.965Z"></path>
  </svg>);
const AmazonIcon = ({ className }: {
    className?: string;
}) => (<svg viewBox="0 0 24 24" className={`inline-block w-[1.1em] h-[1.1em] mr-2 ${className || "fill-muted-foreground"}`}>
    <path fillRule="evenodd" d="M15.62 11.13c-.15.1-.37.18-.64.18-.42 0-.82-.05-1.21-.18l-.22-.04c-.08 0-.1.04-.1.14v.25c0 .08.02.12.05.17.02.03.07.08.15.1.4.18.84.25 1.33.25.52 0 .91-.12 1.24-.37.32-.25.47-.57.47-.99 0-.3-.08-.52-.23-.72-.15-.17-.4-.34-.74-.47l-.7-.27c-.26-.1-.46-.2-.53-.3a.47.47 0 0 1-.15-.36c0-.38.27-.57.84-.57.32 0 .64.05.94.15l.2.04c.07 0 .12-.04.12-.14v-.25c0-.08-.03-.12-.05-.17-.03-.05-.08-.08-.15-.1-.37-.13-.74-.2-1.11-.2-.47 0-.87.12-1.16.35-.3.22-.45.54-.45.91 0 .57.32.99.97 1.24l.74.27c.24.1.4.17.5.27.09.1.12.2.12.35 0 .2-.08.37-.23.46Zm-3.88-3.55v3.28c-.42.28-.84.42-1.26.42-.27 0-.47-.07-.6-.22-.11-.15-.16-.37-.16-.7V7.59c0-.13-.05-.18-.18-.18h-.52c-.12 0-.17.05-.17.18v3.06c0 .42.1.77.32.99.22.22.55.35.97.35.56 0 1.13-.2 1.68-.6l.05.3c0 .07.02.1.07.12.02.03.07.03.15.03h.37c.12 0 .17-.05.17-.18V7.58c0-.13-.05-.18-.17-.18h-.52c-.15 0-.2.08-.2.18Zm-4.69 4.27h.52c.12 0 .17-.05.17-.17v-3.1c0-.41-.1-.73-.32-.95a1.25 1.25 0 0 0-.94-.35c-.57 0-1.16.2-1.73.62-.2-.42-.57-.62-1.11-.62-.55 0-1.1.2-1.64.57l-.04-.27c0-.08-.03-.1-.08-.13-.02-.02-.07-.02-.12-.02h-.4c-.12 0-.17.05-.17.17v4.1c0 .13.05.18.17.18h.52c.12 0 .17-.05.17-.18V8.37c.42-.25.84-.4 1.29-.4.25 0 .42.08.52.22.1.15.17.35.17.65v2.84c0 .12.05.17.17.17h.52c.13 0 .18-.05.18-.17V8.37c.44-.27.86-.4 1.28-.4.25 0 .42.08.52.22.1.15.17.35.17.65v2.84c0 .12.05.17.18.17Zm13.47 3.29a21.8 21.8 0 0 1-8.3 1.7c-3.96 0-7.8-1.08-10.88-2.89a.35.35 0 0 0-.15-.05c-.17 0-.27.2-.1.37a16.11 16.11 0 0 0 10.87 4.16c3.02 0 6.5-.94 8.9-2.72.42-.3.08-.74-.34-.57Zm-.08-6.74c.22-.26.57-.38 1.06-.38.25 0 .5.03.72.1l.15.02c.07 0 .12-.04.12-.17v-.25c0-.07-.02-.14-.05-.17a.54.54 0 0 0-.12-.1c-.32-.07-.64-.15-.94-.15-.7 0-1.21.2-1.6.62-.38.4-.57 1-.57 1.73 0 .74.17 1.31.54 1.7.37.4.89.6 1.58.6.37 0 .72-.05.99-.17.07-.03.12-.05.15-.1.02-.03.02-.1.02-.17v-.25c0-.13-.05-.17-.12-.17-.03 0-.07 0-.12.02-.28.07-.55.12-.8.12-.46 0-.81-.12-1.03-.37-.23-.24-.32-.64-.32-1.16v-.12c.02-.55.12-.94.34-1.19Z" clipRule="evenodd"></path>
    <path fillRule="evenodd" d="M21.55 17.46c1.29-1.09 1.64-3.33 1.36-3.68-.12-.15-.71-.3-1.45-.3-.8 0-1.73.18-2.45.67-.22.15-.17.35.05.32.76-.1 2.5-.3 2.82.1.3.4-.35 2.03-.65 2.74-.07.23.1.3.32.15ZM18.12 7.4h-.52c-.12 0-.17.05-.17.18v4.1c0 .12.05.17.17.17h.52c.12 0 .17-.05.17-.17v-4.1c0-.1-.05-.18-.17-.18Zm.15-1.68a.58.58 0 0 0-.42-.15c-.18 0-.3.05-.4.15a.5.5 0 0 0-.15.37c0 .15.05.3.15.37.1.1.22.15.4.15.17 0 .3-.05.4-.15a.5.5 0 0 0 .14-.37c0-.15-.02-.3-.12-.37Z" clipRule="evenodd"></path>
  </svg>);
const DeezerIcon = ({ className }: {
    className?: string;
}) => (<svg viewBox="0 0 512 512" className={`inline-block w-[1.1em] h-[1.1em] mr-2 ${className || "fill-muted-foreground"}`}>
    <path fill="currentColor" d="M14.8 101.1C6.6 101.1 0 127.6 0 160.3s6.6 59.2 14.8 59.2s14.8-26.5 14.8-59.2s-6.6-59.2-14.8-59.2m433.9-60.2c-7.7 0-14.5 17.1-19.4 44.1c-7.7-46.7-20.2-77-34.2-77c-16.8 0-31.1 42.9-38 105.4c-6.6-45.4-16.8-74.2-28.3-74.2c-16.1 0-29.6 56.9-34.7 136.2c-9.4-40.8-23.2-66.3-38.3-66.3s-28.8 25.5-38.3 66.3c-5.1-79.3-18.6-136.2-34.7-136.2c-11.5 0-21.7 28.8-28.3 74.2C147.9 50.9 133.3 8 116.7 8c-14 0-26.5 30.4-34.2 77c-4.8-27-11.7-44.1-19.4-44.1c-14.3 0-26 59.2-26 132.1S49 305.2 63.3 305.2c5.9 0 11.5-9.9 15.8-26.8c6.9 61.7 21.2 104.1 38 104.1c13 0 24.5-25.5 32.1-65.6c5.4 76.3 18.6 130.4 34.2 130.4c9.7 0 18.6-21.4 25.3-56.4c7.9 72.2 26.3 122.7 47.7 122.7s39.5-50.5 47.7-122.7c6.6 35 15.6 56.4 25.3 56.4c15.6 0 28.8-54.1 34.2-130.4c7.7 40.1 19.4 65.6 32.1 65.6c16.6 0 30.9-42.3 38-104.1c4.3 16.8 9.7 26.8 15.8 26.8c14.3 0 26-59.2 26-132.1S463 40.9 448.7 40.9m48.5 60.2c-8.2 0-14.8 26.5-14.8 59.2s6.6 59.2 14.8 59.2S512 193 512 160.3s-6.6-59.2-14.8-59.2"/>
  </svg>);
interface SettingsPageProps {
    onUnsavedChangesChange?: (hasUnsavedChanges: boolean) => void;
    onResetRequest?: (resetFn: () => void) => void;
}
export function SettingsPage({ onUnsavedChangesChange, onResetRequest, }: SettingsPageProps) {
    const [savedSettings, setSavedSettings] = useState<SettingsType>(getSettings());
    const [tempSettings, setTempSettings] = useState<SettingsType>(savedSettings);
    const [isDark, setIsDark] = useState(document.documentElement.classList.contains("dark"));
    const [showResetConfirm, setShowResetConfirm] = useState(false);
    const [showFileBrowser, setShowFileBrowser] = useState(false);
    const hasUnsavedChanges = JSON.stringify(savedSettings) !== JSON.stringify(tempSettings);
    const resetToSaved = useCallback(() => {
        const freshSavedSettings = getSettings();
        flushSync(() => {
            setTempSettings(freshSavedSettings);
            setIsDark(document.documentElement.classList.contains("dark"));
        });
    }, []);
    useEffect(() => {
        if (onResetRequest) {
            onResetRequest(resetToSaved);
        }
    }, [onResetRequest, resetToSaved]);
    useEffect(() => {
        let cancelled = false;
        loadSettings().then((s) => { if (!cancelled) { setSavedSettings(s); setTempSettings(s); } });
        return () => { cancelled = true; };
    }, []); // sync from backend on mount
    useEffect(() => {
        onUnsavedChangesChange?.(hasUnsavedChanges);
    }, [hasUnsavedChanges, onUnsavedChangesChange]);
    useEffect(() => {
        applyThemeMode(savedSettings.themeMode);
        applyTheme(savedSettings.theme);
        const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
        const handleChange = () => {
            if (savedSettings.themeMode === "auto") {
                applyThemeMode("auto");
                applyTheme(savedSettings.theme);
            }
        };
        mediaQuery.addEventListener("change", handleChange);
        return () => mediaQuery.removeEventListener("change", handleChange);
    }, [savedSettings.themeMode, savedSettings.theme]);
    useEffect(() => {
        applyThemeMode(tempSettings.themeMode);
        applyTheme(tempSettings.theme);
        applyFont(tempSettings.fontFamily);
        setTimeout(() => {
            setIsDark(document.documentElement.classList.contains("dark"));
        }, 0);
    }, [tempSettings.themeMode, tempSettings.theme, tempSettings.fontFamily]);
    useEffect(() => {
        const loadDefaults = async () => {
            if (!savedSettings.downloadPath) {
                const settingsWithDefaults = await getSettingsWithDefaults();
                setSavedSettings(settingsWithDefaults);
                setTempSettings(settingsWithDefaults);
                await saveSettings(settingsWithDefaults);
            }
        };
        loadDefaults();
    }, []);
    const handleSave = async () => {
        await saveSettings(tempSettings);
        setSavedSettings(tempSettings);
        toast.success("Settings saved");
        onUnsavedChangesChange?.(false);
    };
    const handleReset = async () => {
        const defaultSettings = await resetToDefaultSettings();
        setTempSettings(defaultSettings);
        setSavedSettings(defaultSettings);
        applyThemeMode(defaultSettings.themeMode);
        applyTheme(defaultSettings.theme);
        applyFont(defaultSettings.fontFamily);
        setShowResetConfirm(false);
        toast.success("Settings reset to default");
    };
    const handleBrowseFolder = () => setShowFileBrowser(true);
    const handleTidalQualityChange = async (value: "LOSSLESS" | "HI_RES_LOSSLESS") => {
        setTempSettings((prev) => ({ ...prev, tidalQuality: value }));
    };
    const handleQobuzQualityChange = (value: "6" | "7" | "27") => {
        setTempSettings((prev) => ({ ...prev, qobuzQuality: value }));
    };
    const handleAutoQualityChange = async (value: "16" | "24") => {
        setTempSettings((prev) => ({ ...prev, autoQuality: value }));
    };
    const [activeTab, setActiveTab] = useState<"general" | "files" | "keys" | "tidal" | "apis">("general");

    // ── API Keys state ───────────────────────────────────────────────────────
    const [apiKeys, setApiKeys] = useState<APIKeyMeta[]>([]);
    const [newKeyName, setNewKeyName] = useState("");
    const [createdKey, setCreatedKey] = useState<CreatedAPIKey | null>(null);
    const [copiedKey, setCopiedKey] = useState(false);
    const [keysLoading, setKeysLoading] = useState(false);

    const loadApiKeys = useCallback(async () => {
        setKeysLoading(true);
        try { setApiKeys(await ListAPIKeys()); } catch { /* ignore */ } finally { setKeysLoading(false); }
    }, []);

    useEffect(() => { if (activeTab === "keys") loadApiKeys(); }, [activeTab, loadApiKeys]);

    const handleCreateKey = async () => {
        if (!newKeyName.trim()) return;
        try {
            const result = await CreateAPIKey(newKeyName.trim(), ["read", "download"]);
            setCreatedKey(result);
            setNewKeyName("");
            loadApiKeys();
        } catch (err) {
            toast.error("Failed to create key", { description: err instanceof Error ? err.message : "Unknown error" });
        }
    };

    const handleRevokeKey = async (id: string) => {
        try {
            await DeleteAPIKey(id);
            setApiKeys(prev => prev.filter(k => k.id !== id));
        } catch (err) {
            toast.error("Failed to revoke key", { description: err instanceof Error ? err.message : "Unknown error" });
        }
    };

    const handleCopyKey = () => {
        if (!createdKey) return;
        navigator.clipboard.writeText(createdKey.key);
        setCopiedKey(true);
        setTimeout(() => setCopiedKey(false), 2000);
    };

    // ── Tidal Auth state ─────────────────────────────────────────────────────
    const [tidalStatus, setTidalStatus] = useState<TidalStatus | null>(null);
    const [tidalCallbackURL, setTidalCallbackURL] = useState("");
    const [tidalConnecting, setTidalConnecting] = useState(false);
    const [showCallbackInput, setShowCallbackInput] = useState(false);

    const loadTidalStatus = useCallback(async () => {
        try { setTidalStatus(await GetTidalStatus()); } catch { /* ignore */ }
    }, []);

    useEffect(() => { if (activeTab === "tidal") loadTidalStatus(); }, [activeTab, loadTidalStatus]);

    const handleTidalConnect = async () => {
        try {
            const url = await GetTidalAuthURL();
            window.open(url, "_blank");
            setShowCallbackInput(true);
        } catch (err) {
            toast.error("Failed to get Tidal auth URL", { description: err instanceof Error ? err.message : "Unknown error" });
        }
    };

    const handleTidalCallback = async () => {
        if (!tidalCallbackURL.trim()) return;
        setTidalConnecting(true);
        try {
            await SubmitTidalCallback(tidalCallbackURL.trim());
            setTidalCallbackURL("");
            setShowCallbackInput(false);
            await loadTidalStatus();
            toast.success("Tidal account connected");
        } catch (err) {
            toast.error("Failed to connect Tidal", { description: err instanceof Error ? err.message : "Unknown error" });
        } finally {
            setTidalConnecting(false);
        }
    };

    const handleTidalDisconnect = async () => {
        try {
            await DisconnectTidal();
            setTidalStatus({ connected: false });
            setShowCallbackInput(false);
            toast.success("Tidal account disconnected");
        } catch (err) {
            toast.error("Failed to disconnect", { description: err instanceof Error ? err.message : "Unknown error" });
        }
    };

    // ── API Statuses state ───────────────────────────────────────────────────
    const [apiStatuses, setApiStatuses] = useState<ServiceStatus[] | null>(null);
    const [apisLoading, setApisLoading] = useState(false);

    const loadApiStatuses = useCallback(async () => {
        setApisLoading(true);
        try { setApiStatuses(await GetAPIStatuses()); }
        catch (err) { toast.error("Status check failed", { description: err instanceof Error ? err.message : "Unknown error" }); }
        finally { setApisLoading(false); }
    }, []);

    useEffect(() => { if (activeTab === "apis") { loadApiStatuses(); loadProxies(); } }, [activeTab, loadApiStatuses]);

    // ── Proxy config state ───────────────────────────────────────────────────
    const [proxies, setProxies] = useState<ProxyConfig | null>(null);
    const [proxySaving, setProxySaving] = useState(false);
    const [newTidalProxy, setNewTidalProxy] = useState("");

    const loadProxies = useCallback(async () => {
        try { setProxies(await GetAPIProxies()); } catch { /* ignore */ }
    }, []);

    const handleSaveProxies = async () => {
        if (!proxies) return;
        setProxySaving(true);
        try {
            await UpdateAPIProxies(proxies);
            toast.success("Proxy configuration saved");
        } catch (err) {
            toast.error("Failed to save", { description: err instanceof Error ? err.message : "Unknown error" });
        } finally {
            setProxySaving(false);
        }
    };

    const handleAddTidalProxy = () => {
        const url = newTidalProxy.trim();
        if (!url || !proxies) return;
        setProxies(prev => prev ? { ...prev, tidal_proxies: [...prev.tidal_proxies, url] } : prev);
        setNewTidalProxy("");
    };

    const handleRemoveTidalProxy = (idx: number) => {
        setProxies(prev => prev ? { ...prev, tidal_proxies: prev.tidal_proxies.filter((_, i) => i !== idx) } : prev);
    };

    return (<div className="space-y-4 h-full flex flex-col">
      <div className="flex items-center justify-between shrink-0">
        <h1 className="text-2xl font-bold">Settings</h1>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setShowResetConfirm(true)} className="gap-1.5">
            <RotateCcw className="h-4 w-4"/>
            Reset to Default
          </Button>
          <Button onClick={handleSave} className="gap-1.5">
            <Save className="h-4 w-4"/>
            Save Changes
          </Button>
        </div>
      </div>

      <div className="flex gap-2 border-b shrink-0">
        <Button variant={activeTab === "general" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("general")} className="rounded-b-none gap-2">
          <Settings className="h-4 w-4"/>
          General
        </Button>
        <Button variant={activeTab === "files" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("files")} className="rounded-b-none gap-2">
          <FolderCog className="h-4 w-4"/>
          File Management
        </Button>
        <Button variant={activeTab === "keys" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("keys")} className="rounded-b-none gap-2">
          <Key className="h-4 w-4"/>
          API Keys
        </Button>
        <Button variant={activeTab === "tidal" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("tidal")} className="rounded-b-none gap-2">
          <TidalIcon className={activeTab === "tidal" ? "fill-foreground" : undefined}/>
          Tidal Account
        </Button>
        <Button variant={activeTab === "apis" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("apis")} className="rounded-b-none gap-2">
          <Link className="h-4 w-4"/>
          APIs
        </Button>
      </div>

      <div className="flex-1 overflow-y-auto pt-4">
        {activeTab === "general" && (<div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="download-path">Download Path</Label>
                <div className="flex gap-2">
                  <InputWithContext id="download-path" value={tempSettings.downloadPath} onChange={(e) => setTempSettings((prev) => ({
                ...prev,
                downloadPath: e.target.value,
            }))} placeholder="C:\Users\YourUsername\Music"/>
                  <Button type="button" onClick={handleBrowseFolder} className="gap-1.5">
                    <FolderOpen className="h-4 w-4"/>
                    Browse
                  </Button>
                </div>
              </div>

              <div className="space-y-2">
                <Label htmlFor="theme-mode">Mode</Label>
                <Select value={tempSettings.themeMode} onValueChange={(value: "auto" | "light" | "dark") => setTempSettings((prev) => ({ ...prev, themeMode: value }))}>
                  <SelectTrigger id="theme-mode">
                    <SelectValue placeholder="Select theme mode"/>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">Auto</SelectItem>
                    <SelectItem value="light">Light</SelectItem>
                    <SelectItem value="dark">Dark</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="theme">Accent</Label>
                <Select value={tempSettings.theme} onValueChange={(value) => setTempSettings((prev) => ({ ...prev, theme: value }))}>
                  <SelectTrigger id="theme">
                    <SelectValue placeholder="Select a theme"/>
                  </SelectTrigger>
                  <SelectContent>
                    {themes.map((theme) => (<SelectItem key={theme.name} value={theme.name}>
                        <span className="flex items-center gap-2">
                          <span className="w-3 h-3 rounded-full border border-border" style={{
                    backgroundColor: isDark
                        ? theme.cssVars.dark.primary
                        : theme.cssVars.light.primary,
                }}/>
                          {theme.label}
                        </span>
                      </SelectItem>))}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="font">Font</Label>
                <Select value={tempSettings.fontFamily} onValueChange={(value: FontFamily) => setTempSettings((prev) => ({ ...prev, fontFamily: value }))}>
                  <SelectTrigger id="font">
                    <SelectValue placeholder="Select a font"/>
                  </SelectTrigger>
                  <SelectContent>
                    {FONT_OPTIONS.map((font) => (<SelectItem key={font.value} value={font.value}>
                        <span style={{ fontFamily: font.fontFamily }}>
                          {font.label}
                        </span>
                      </SelectItem>))}
                  </SelectContent>
                </Select>
              </div>

              <div className="flex items-center gap-3 pt-2">
                <Switch id="sfx-enabled" checked={tempSettings.sfxEnabled} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                sfxEnabled: checked,
            }))}/>
                <Label htmlFor="sfx-enabled" className="cursor-pointer text-sm font-normal">
                  Sound Effects
                </Label>
              </div>
            </div>

            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="downloader">Source</Label>
                <div className="flex gap-2 flex-wrap">
                  <Select value={tempSettings.downloader} onValueChange={(value: any) => setTempSettings((prev) => ({
                ...prev,
                downloader: value,
            }))}>
                    <SelectTrigger id="downloader" className="h-9 w-fit">
                      <SelectValue placeholder="Select a source"/>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="auto">Auto</SelectItem>
                      <SelectItem value="tidal">
                        <span className="flex items-center">
                          <TidalIcon />
                          Tidal
                        </span>
                      </SelectItem>
                      <SelectItem value="qobuz">
                        <span className="flex items-center">
                          <QobuzIcon />
                          Qobuz
                        </span>
                      </SelectItem>
                      <SelectItem value="amazon">
                        <span className="flex items-center">
                          <AmazonIcon />
                          Amazon Music
                        </span>
                      </SelectItem>
                      <SelectItem value="deezer" disabled>
                        <span className="flex items-center opacity-50">
                          <DeezerIcon />
                          Deezer (unavailable)
                        </span>
                      </SelectItem>
                    </SelectContent>
                  </Select>

                  {tempSettings.downloader === "auto" && (<>
                      <Select value={tempSettings.autoOrder || "tidal-qobuz-amazon"} onValueChange={(value: any) => setTempSettings((prev) => ({
                    ...prev,
                    autoOrder: value,
                }))}>
                        <SelectTrigger className="h-9 w-fit min-w-[140px]">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          
                          <SelectItem value="tidal-qobuz-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz-deezer-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-tidal-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-tidal-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz-amazon-tidal">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-amazon-tidal-qobuz">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                            </span>
                          </SelectItem>

                          
                          <SelectItem value="tidal-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>

                          
                          <SelectItem value="tidal-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <DeezerIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-tidal">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-tidal">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <AmazonIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <TidalIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-qobuz">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current"/>
                              <ArrowRight className="h-3 w-3 text-muted-foreground"/>
                              <QobuzIcon className="fill-current"/>
                            </span>
                          </SelectItem>
                        </SelectContent>
                      </Select>

                      <Select value={tempSettings.autoQuality || "16"} onValueChange={handleAutoQualityChange}>
                        <SelectTrigger className="h-9 w-fit">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="16">16-bit/44.1kHz</SelectItem>
                          <SelectItem value="24">24-bit/48kHz</SelectItem>
                        </SelectContent>
                      </Select>
                    </>)}

                  {tempSettings.downloader === "tidal" && (<Select value={tempSettings.tidalQuality} onValueChange={handleTidalQualityChange}>
                      <SelectTrigger className="h-9 w-fit">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="LOSSLESS">16-bit/44.1kHz</SelectItem>
                        <SelectItem value="HI_RES_LOSSLESS">
                          24-bit/48kHz
                        </SelectItem>
                      </SelectContent>
                    </Select>)}

                  {tempSettings.downloader === "qobuz" && (<Select value={tempSettings.qobuzQuality} onValueChange={handleQobuzQualityChange}>
                      <SelectTrigger className="h-9 w-fit">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="6">16-bit/44.1kHz</SelectItem>
                        <SelectItem value="27">24-bit/48kHz - 192kHz</SelectItem>
                      </SelectContent>
                    </Select>)}

                  {tempSettings.downloader === "amazon" && (<div className="h-9 px-3 flex items-center text-sm font-medium border border-input rounded-md bg-muted/30 text-muted-foreground whitespace-nowrap cursor-default">
                      16-bit - 24-bit/44.1kHz - 192kHz
                    </div>)}
                  {tempSettings.downloader === "deezer" && (<div className="h-9 px-3 flex items-center text-sm font-medium border border-input rounded-md bg-muted/30 text-muted-foreground whitespace-nowrap cursor-default">
                      16-bit/44.1kHz
                    </div>)}
                </div>

                {((tempSettings.downloader === "tidal" &&
                tempSettings.tidalQuality === "HI_RES_LOSSLESS") ||
                (tempSettings.downloader === "qobuz" &&
                    tempSettings.qobuzQuality === "27") ||
                (tempSettings.downloader === "auto" &&
                    tempSettings.autoQuality === "24")) && (<div className="flex items-center gap-3 pt-2">
                    <div className="flex items-center gap-3">
                      <Switch id="allow-fallback" checked={tempSettings.allowFallback} onCheckedChange={(checked) => setTempSettings((prev) => ({
                    ...prev,
                    allowFallback: checked,
                }))}/>
                      <Label htmlFor="allow-fallback" className="text-sm font-normal cursor-pointer">
                        Allow Quality Fallback (16-bit)
                      </Label>
                    </div>
                  </div>)}
              </div>

              <div className="border-t pt-6"/>

              <div className="space-y-4">
                <div className="flex items-center gap-3">
                  <Switch id="embed-lyrics" checked={tempSettings.embedLyrics} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                embedLyrics: checked,
            }))}/>
                  <Label htmlFor="embed-lyrics" className="cursor-pointer text-sm font-normal">
                    Embed Lyrics
                  </Label>
                </div>
                <div className="flex items-center gap-3">
                  <Switch id="embed-max-quality-cover" checked={tempSettings.embedMaxQualityCover} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                embedMaxQualityCover: checked,
            }))}/>
                  <Label htmlFor="embed-max-quality-cover" className="cursor-pointer text-sm font-normal">
                    Embed Max Quality Cover
                  </Label>
                </div>
                <div className="flex items-center gap-3">
                  <Switch id="embed-genre" checked={tempSettings.embedGenre} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                embedGenre: checked,
            }))}/>
                  <Label htmlFor="embed-genre" className="cursor-pointer text-sm font-normal">
                    Embed Genre
                  </Label>
                </div>
                {tempSettings.embedGenre && (<div className="flex items-center gap-3">
                    <Switch id="use-single-genre" checked={tempSettings.useSingleGenre} onCheckedChange={(checked) => setTempSettings((prev) => ({
                    ...prev,
                    useSingleGenre: checked,
                }))}/>
                    <Label htmlFor="use-single-genre" className="text-sm cursor-pointer font-normal">
                      Use Single Genre
                    </Label>
                  </div>)}
              </div>
            </div>
          </div>)}

        {activeTab === "files" && (<div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-4">
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <Label className="text-sm">Folder Structure</Label>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Info className="h-3.5 w-3.5 text-muted-foreground cursor-help"/>
                    </TooltipTrigger>
                    <TooltipContent side="top">
                      <p className="text-xs whitespace-nowrap">
                        Variables:{" "}
                        {TEMPLATE_VARIABLES.map((v) => v.key).join(", ")}
                      </p>
                    </TooltipContent>
                  </Tooltip>
                </div>
                <div className="flex gap-2">
                  <Select value={tempSettings.folderPreset} onValueChange={(value: FolderPreset) => {
                const preset = FOLDER_PRESETS[value];
                setTempSettings((prev) => ({
                    ...prev,
                    folderPreset: value,
                    folderTemplate: value === "custom"
                        ? prev.folderTemplate || preset.template
                        : preset.template,
                }));
            }}>
                    <SelectTrigger className="h-9 w-fit">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {Object.entries(FOLDER_PRESETS).map(([key, { label }]) => (<SelectItem key={key} value={key}>
                            {label}
                          </SelectItem>))}
                    </SelectContent>
                  </Select>
                  {tempSettings.folderPreset === "custom" && (<InputWithContext value={tempSettings.folderTemplate} onChange={(e) => setTempSettings((prev) => ({
                    ...prev,
                    folderTemplate: e.target.value,
                }))} placeholder="{artist}/{album}" className="h-9 text-sm flex-1"/>)}
                </div>
                {tempSettings.folderTemplate && (<p className="text-xs text-muted-foreground">
                    Preview:{" "}
                    <span className="font-mono">
                      {tempSettings.folderTemplate
                    .replace(/\{artist\}/g, "Kendrick Lamar, SZA")
                    .replace(/\{album\}/g, "Black Panther")
                    .replace(/\{album_artist\}/g, "Kendrick Lamar")
                    .replace(/\{year\}/g, "2018")
                    .replace(/\{date\}/g, "2018-02-09")}
                      /
                    </span>
                  </p>)}
              </div>

              <div className="flex items-center gap-3">
                <Switch id="create-playlist-folder" checked={tempSettings.createPlaylistFolder} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                createPlaylistFolder: checked,
            }))}/>
                <Label htmlFor="create-playlist-folder" className="text-sm cursor-pointer font-normal">
                  Playlist Folder
                </Label>
              </div>

              <div className="flex items-center gap-3">
                <Switch id="create-m3u8-file" checked={tempSettings.createM3u8File} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                createM3u8File: checked,
            }))}/>
                <Label htmlFor="create-m3u8-file" className="text-sm cursor-pointer font-normal">
                  Create M3U8 Playlist File
                </Label>
              </div>
              {tempSettings.createM3u8File && (
                <div className="ml-0 pl-0 space-y-2 border-l-2 border-muted pl-4">
                  <div className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      id="jellyfin-m3u8"
                      checked={!!tempSettings.jellyfinMusicPath}
                      onChange={(e) => setTempSettings((prev) => ({
                        ...prev,
                        jellyfinMusicPath: e.target.checked ? "/Multimedia/Musique/Spotiflac" : "",
                      }))}
                      className="rounded"
                    />
                    <Label htmlFor="jellyfin-m3u8" className="text-sm cursor-pointer font-normal">
                      Jellyfin compatible paths
                    </Label>
                  </div>
                  {!!tempSettings.jellyfinMusicPath && (
                    <div className="flex flex-col gap-1">
                      <Label className="text-xs text-muted-foreground">Jellyfin music library path</Label>
                      <input
                        type="text"
                        value={tempSettings.jellyfinMusicPath}
                        onChange={(e) => setTempSettings((prev) => ({ ...prev, jellyfinMusicPath: e.target.value }))}
                        placeholder="/Multimedia/Musique/Spotiflac"
                        className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm font-mono"
                      />
                      <p className="text-xs text-muted-foreground">Path as seen by Jellyfin (replaces /home/nonroot/Music in M3U8 files)</p>
                    </div>
                  )}
                </div>
              )}

              <div className="flex items-center gap-3">
                <Switch id="use-first-artist-only" checked={tempSettings.useFirstArtistOnly} onCheckedChange={(checked) => setTempSettings((prev) => ({
                ...prev,
                useFirstArtistOnly: checked,
            }))}/>
                <Label htmlFor="use-first-artist-only" className="text-sm cursor-pointer font-normal">
                  Use First Artist Only
                </Label>
              </div>


            </div>

            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <Label className="text-sm">Filename Format</Label>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Info className="h-3.5 w-3.5 text-muted-foreground cursor-help"/>
                  </TooltipTrigger>
                  <TooltipContent side="top">
                    <p className="text-xs whitespace-nowrap">
                      Variables:{" "}
                      {TEMPLATE_VARIABLES.map((v) => v.key).join(", ")}
                    </p>
                  </TooltipContent>
                </Tooltip>
              </div>
              <div className="flex gap-2">
                <Select value={tempSettings.filenamePreset} onValueChange={(value: FilenamePreset) => {
                const preset = FILENAME_PRESETS[value];
                setTempSettings((prev) => ({
                    ...prev,
                    filenamePreset: value,
                    filenameTemplate: value === "custom"
                        ? prev.filenameTemplate || preset.template
                        : preset.template,
                }));
            }}>
                  <SelectTrigger className="h-9 w-fit">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.entries(FILENAME_PRESETS).map(([key, { label }]) => (<SelectItem key={key} value={key}>
                          {label}
                        </SelectItem>))}
                  </SelectContent>
                </Select>
                {tempSettings.filenamePreset === "custom" && (<InputWithContext value={tempSettings.filenameTemplate} onChange={(e) => setTempSettings((prev) => ({
                    ...prev,
                    filenameTemplate: e.target.value,
                }))} placeholder="{track}. {title}" className="h-9 text-sm flex-1"/>)}
              </div>
              {tempSettings.filenameTemplate && (<p className="text-xs text-muted-foreground">
                  Preview:{" "}
                  <span className="font-mono">
                    {tempSettings.filenameTemplate
                    .replace(/\{artist\}/g, "Kendrick Lamar, SZA")
                    .replace(/\{album_artist\}/g, "Kendrick Lamar")
                    .replace(/\{title\}/g, "All The Stars")
                    .replace(/\{track\}/g, "01")
                    .replace(/\{disc\}/g, "1")
                    .replace(/\{year\}/g, "2018")
                    .replace(/\{date\}/g, "2018-02-09")}
                    .flac
                  </span>
                </p>)}
            </div>
          </div>)}

        {activeTab === "keys" && (<div className="space-y-6 max-w-2xl">
          <div>
            <h2 className="text-base font-semibold mb-1">Personal API Keys</h2>
            <p className="text-sm text-muted-foreground mb-4">
              Create API keys to use SpotiFLAC from external applications. Pass the key as the
              <code className="mx-1 px-1 rounded bg-muted font-mono text-xs">X-API-Key</code> header.
            </p>
            <div className="flex gap-2">
              <InputWithContext value={newKeyName} onChange={e => setNewKeyName(e.target.value)}
                placeholder="Key name (e.g. my-app)" className="flex-1"
                onKeyDown={e => e.key === "Enter" && handleCreateKey()}/>
              <Button onClick={handleCreateKey} disabled={!newKeyName.trim()} className="gap-1.5">
                <Key className="h-4 w-4"/>
                Create Key
              </Button>
            </div>
          </div>

          {keysLoading ? (<div className="text-sm text-muted-foreground">Loading...</div>) : apiKeys.length === 0 ? (
            <div className="text-sm text-muted-foreground border rounded-lg p-4 text-center">No API keys yet.</div>
          ) : (
            <div className="space-y-2">
              {apiKeys.map(key => (
                <div key={key.id} className="flex items-center justify-between border rounded-lg px-4 py-3 bg-muted/20">
                  <div className="space-y-0.5">
                    <p className="text-sm font-medium">{key.name}</p>
                    <p className="text-xs text-muted-foreground font-mono">
                      ···{key.id.slice(-8)} &nbsp;·&nbsp;
                      Created {new Date(key.created_at).toLocaleDateString()} &nbsp;·&nbsp;
                      {key.permissions.join(", ")}
                    </p>
                  </div>
                  <Button variant="ghost" size="sm" onClick={() => handleRevokeKey(key.id)} className="text-destructive hover:text-destructive gap-1">
                    <Trash2 className="h-4 w-4"/>
                    Revoke
                  </Button>
                </div>
              ))}
            </div>
          )}
          <Button variant="outline" size="sm" onClick={loadApiKeys} disabled={keysLoading} className="gap-1.5">
            <RefreshCw className="h-3.5 w-3.5"/>
            Refresh
          </Button>
        </div>)}

        {activeTab === "tidal" && (<div className="space-y-6 max-w-2xl">
          <div>
            <h2 className="text-base font-semibold mb-1">Tidal Account</h2>
            <p className="text-sm text-muted-foreground mb-4">
              Connect your personal Tidal account to download tracks without relying on community proxies.
            </p>
          </div>

          {tidalStatus === null ? (
            <div className="text-sm text-muted-foreground">Loading...</div>
          ) : tidalStatus.connected ? (
            <div className="space-y-4">
              <div className="flex items-center gap-3 border rounded-lg px-4 py-3 bg-muted/20">
                <span className="h-2.5 w-2.5 rounded-full bg-green-500 shrink-0"/>
                <div className="flex-1">
                  <p className="text-sm font-medium">Connected</p>
                  {tidalStatus.expires_at && (
                    <p className="text-xs text-muted-foreground">
                      Expires {new Date(tidalStatus.expires_at * 1000).toLocaleDateString()}
                    </p>
                  )}
                </div>
                <Button variant="outline" size="sm" onClick={handleTidalDisconnect} className="gap-1.5 text-destructive hover:text-destructive">
                  <Link className="h-4 w-4"/>
                  Disconnect
                </Button>
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="flex items-center gap-3 border rounded-lg px-4 py-3 bg-muted/20">
                <span className="h-2.5 w-2.5 rounded-full bg-muted-foreground shrink-0"/>
                <p className="text-sm flex-1">Not connected</p>
                <Button size="sm" onClick={handleTidalConnect} className="gap-1.5">
                  <ExternalLink className="h-4 w-4"/>
                  Connect with Tidal
                </Button>
              </div>

              {showCallbackInput && (
                <div className="space-y-2">
                  <Label className="text-sm">After authorizing, paste the full redirect URL here:</Label>
                  <div className="flex gap-2">
                    <InputWithContext value={tidalCallbackURL} onChange={e => setTidalCallbackURL(e.target.value)}
                      placeholder="https://login.tidal.com/..." className="flex-1 font-mono text-xs"/>
                    <Button onClick={handleTidalCallback} disabled={!tidalCallbackURL.trim() || tidalConnecting} className="gap-1.5">
                      {tidalConnecting ? <RefreshCw className="h-4 w-4 animate-spin"/> : <Check className="h-4 w-4"/>}
                      Submit
                    </Button>
                  </div>
                </div>
              )}
            </div>
          )}
          <Button variant="outline" size="sm" onClick={loadTidalStatus} className="gap-1.5">
            <RefreshCw className="h-3.5 w-3.5"/>
            Refresh Status
          </Button>
        </div>)}

        {activeTab === "apis" && (<div className="space-y-6 max-w-2xl">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-base font-semibold mb-1">External APIs</h2>
              <p className="text-sm text-muted-foreground">Status of all external services used by SpotiFLAC. Results are cached for 30 seconds.</p>
            </div>
            <Button variant="outline" size="sm" onClick={loadApiStatuses} disabled={apisLoading} className="gap-1.5 shrink-0">
              <RefreshCw className={`h-3.5 w-3.5 ${apisLoading ? "animate-spin" : ""}`}/>
              {apisLoading ? "Checking..." : "Refresh"}
            </Button>
          </div>

          {apiStatuses === null ? (
            apisLoading
              ? <div className="text-sm text-muted-foreground">Checking all services...</div>
              : <div className="text-sm text-muted-foreground">Click Refresh to check service status.</div>
          ) : (
            <div className="space-y-1.5">
              {apiStatuses.map(svc => (
                <div key={svc.name} className="flex items-center gap-3 border rounded-lg px-3 py-2.5 bg-muted/10">
                  <span className={`h-2 w-2 rounded-full shrink-0 ${
                    svc.status === "ok" ? "bg-green-500" :
                    svc.status === "ratelimited" ? "bg-yellow-500" :
                    svc.status === "unconfigured" ? "bg-muted-foreground" : "bg-red-500"
                  }`}/>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium truncate">{svc.name}</p>
                    <p className="text-xs text-muted-foreground truncate">{svc.url}</p>
                    {svc.error && <p className="text-xs text-destructive truncate">{svc.error}</p>}
                  </div>
                  <div className="text-right shrink-0">
                    <p className={`text-xs font-medium ${
                      svc.status === "ok" ? "text-green-600 dark:text-green-400" :
                      svc.status === "ratelimited" ? "text-yellow-600 dark:text-yellow-400" : "text-red-600 dark:text-red-400"
                    }`}>
                      {svc.status === "ok" ? "OK" : svc.status === "ratelimited" ? "Rate limited" : svc.status === "unconfigured" ? "—" : "Down"}
                    </p>
                    {svc.latency_ms !== undefined && <p className="text-xs text-muted-foreground">{svc.latency_ms}ms</p>}
                  </div>
                </div>
              ))}
            </div>
          )}

          {proxies && (<div className="space-y-4 border-t pt-4">
            <h3 className="text-sm font-semibold">Proxy Configuration</h3>

            <div className="space-y-2">
              <Label className="text-sm">Tidal Community Proxies</Label>
              <div className="space-y-1.5">
                {proxies.tidal_proxies.map((p, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <code className="flex-1 text-xs font-mono truncate border rounded px-2 py-1.5 bg-muted/20">{p}</code>
                    <Button variant="ghost" size="sm" onClick={() => handleRemoveTidalProxy(i)} className="text-destructive hover:text-destructive px-2">
                      <Trash2 className="h-3.5 w-3.5"/>
                    </Button>
                  </div>
                ))}
              </div>
              <div className="flex gap-2">
                <InputWithContext value={newTidalProxy} onChange={e => setNewTidalProxy(e.target.value)}
                  placeholder="https://my-proxy.example.com" className="flex-1 font-mono text-xs"
                  onKeyDown={e => e.key === "Enter" && handleAddTidalProxy()}/>
                <Button variant="outline" size="sm" onClick={handleAddTidalProxy} disabled={!newTidalProxy.trim()}>Add</Button>
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label className="text-sm">Amazon Music Proxy</Label>
                <InputWithContext value={proxies.amazon_proxy_base} className="font-mono text-xs"
                  onChange={e => setProxies(prev => prev ? { ...prev, amazon_proxy_base: e.target.value } : prev)}
                  placeholder="https://amzn.afkarxyz.fun"/>
              </div>
              <div className="space-y-1.5">
                <Label className="text-sm">Deezer Proxy</Label>
                <InputWithContext value={proxies.deezer_proxy_base} className="font-mono text-xs"
                  onChange={e => setProxies(prev => prev ? { ...prev, deezer_proxy_base: e.target.value } : prev)}
                  placeholder="https://api.deezmate.com"/>
              </div>
            </div>

            <Button onClick={handleSaveProxies} disabled={proxySaving} className="gap-1.5">
              <Save className="h-4 w-4"/>
              {proxySaving ? "Saving..." : "Save Proxy Config"}
            </Button>
          </div>)}
        </div>)}
      </div>

      <Dialog open={showResetConfirm} onOpenChange={setShowResetConfirm}>
        <DialogContent className="max-w-md [&>button]:hidden">
          <DialogHeader>
            <DialogTitle>Reset to Default?</DialogTitle>
            <DialogDescription>
              This will reset all settings to their default values. Your custom
              configurations will be lost.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowResetConfirm(false)}>
              Cancel
            </Button>
            <Button onClick={handleReset}>Reset</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!createdKey} onOpenChange={() => setCreatedKey(null)}>
        <DialogContent className="max-w-md [&>button]:hidden">
          <DialogHeader>
            <DialogTitle>API Key Created</DialogTitle>
            <DialogDescription>
              Copy this key now — it will not be shown again.
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2 rounded-lg border bg-muted p-3">
            <code className="flex-1 text-xs font-mono break-all">{createdKey?.key}</code>
            <Button variant="ghost" size="sm" onClick={handleCopyKey} className="shrink-0">
              {copiedKey ? <Check className="h-4 w-4 text-green-500"/> : <Copy className="h-4 w-4"/>}
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={() => setCreatedKey(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

    <FileBrowser
        isOpen={showFileBrowser}
        onClose={() => setShowFileBrowser(false)}
        onSelect={(p) => setTempSettings((prev) => ({ ...prev, downloadPath: p }))}
        initialPath={tempSettings.downloadPath}
        title="Select Download Folder"
      />
    </div>);
}
