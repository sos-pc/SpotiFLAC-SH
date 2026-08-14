import { useState, useEffect, useCallback } from "react";
import { flushSync } from "react-dom";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Save,
  RotateCcw,
  Settings,
  FolderCog,
  Key,
  Link,
  Wrench,
} from "lucide-react";
import { FileBrowser } from "@/components/FileBrowser";
import { ApiKeysTab } from "@/components/settings/ApiKeysTab";
import { ApisTab } from "@/components/settings/ApisTab";
import { FilesTab } from "@/components/settings/FilesTab";
import { GeneralTab } from "@/components/settings/GeneralTab";
import { MaintenanceTab } from "@/components/settings/MaintenanceTab";
import { TidalTab } from "@/components/settings/TidalTab";
import { TidalIcon } from "@/components/settings/providerIcons";
import { getUser } from "@/lib/auth";
import {
  getSettings,
  loadSettings,
  getSettingsWithDefaults,
  saveSettings,
  resetToDefaultSettings,
  applyThemeMode,
  applyFont,
  type Settings as SettingsType,
} from "@/lib/settings";
import { applyTheme } from "@/lib/themes";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
interface SettingsPageProps {
  onUnsavedChangesChange?: (hasUnsavedChanges: boolean) => void;
  onResetRequest?: (resetFn: () => void) => void;
}
export function SettingsPage({
  onUnsavedChangesChange,
  onResetRequest,
}: SettingsPageProps) {
  const [savedSettings, setSavedSettings] =
    useState<SettingsType>(getSettings());
  const [tempSettings, setTempSettings] = useState<SettingsType>(savedSettings);
  const [isDark, setIsDark] = useState(
    document.documentElement.classList.contains("dark"),
  );
  const [showResetConfirm, setShowResetConfirm] = useState(false);
  const [showFileBrowser, setShowFileBrowser] = useState(false);
  const hasUnsavedChanges =
    JSON.stringify(savedSettings) !== JSON.stringify(tempSettings);
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
    loadSettings().then((s) => {
      if (!cancelled) {
        setSavedSettings(s);
        setTempSettings(s);
      }
    });
    return () => {
      cancelled = true;
    };
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
  const [activeTab, setActiveTab] = useState<
    "general" | "files" | "keys" | "tidal" | "apis" | "maintenance"
  >("general");

  // isAdmin gates the Maintenance tab (below) and the API-keys "Admin"
  // permission checkbox (passed to ApiKeysTab).
  const isAdmin = getUser()?.is_admin ?? false;

  return (
    <div className="space-y-4 h-full flex flex-col">
      <div className="flex items-center justify-between shrink-0">
        <h1 className="text-2xl font-bold">Settings</h1>
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={() => setShowResetConfirm(true)}
            className="gap-1.5"
          >
            <RotateCcw className="h-4 w-4" />
            Reset to Default
          </Button>
          <Button onClick={handleSave} className="gap-1.5">
            <Save className="h-4 w-4" />
            Save Changes
          </Button>
        </div>
      </div>

      <div className="flex gap-2 border-b shrink-0">
        <Button
          variant={activeTab === "general" ? "default" : "ghost"}
          size="sm"
          onClick={() => setActiveTab("general")}
          className="rounded-b-none gap-2"
        >
          <Settings className="h-4 w-4" />
          General
        </Button>
        <Button
          variant={activeTab === "files" ? "default" : "ghost"}
          size="sm"
          onClick={() => setActiveTab("files")}
          className="rounded-b-none gap-2"
        >
          <FolderCog className="h-4 w-4" />
          File Management
        </Button>
        <Button
          variant={activeTab === "keys" ? "default" : "ghost"}
          size="sm"
          onClick={() => setActiveTab("keys")}
          className="rounded-b-none gap-2"
        >
          <Key className="h-4 w-4" />
          API Keys
        </Button>
        <Button
          variant={activeTab === "tidal" ? "default" : "ghost"}
          size="sm"
          onClick={() => setActiveTab("tidal")}
          className="rounded-b-none gap-2"
        >
          <TidalIcon
            className={activeTab === "tidal" ? "fill-foreground" : undefined}
          />
          Tidal Account
        </Button>
        <Button
          variant={activeTab === "apis" ? "default" : "ghost"}
          size="sm"
          onClick={() => setActiveTab("apis")}
          className="rounded-b-none gap-2"
        >
          <Link className="h-4 w-4" />
          APIs
        </Button>
        {isAdmin && (
          <Button
            variant={activeTab === "maintenance" ? "default" : "ghost"}
            size="sm"
            onClick={() => setActiveTab("maintenance")}
            className="rounded-b-none gap-2"
          >
            <Wrench className="h-4 w-4" />
            Maintenance
          </Button>
        )}
      </div>

      <div className="flex-1 overflow-y-auto pt-4">
        {activeTab === "general" && (
          <GeneralTab
            canEditInstance={isAdmin}
            tempSettings={tempSettings}
            setTempSettings={setTempSettings}
            isDark={isDark}
            handleBrowseFolder={handleBrowseFolder}
          />
        )}

        {activeTab === "files" && (
          <FilesTab tempSettings={tempSettings} setTempSettings={setTempSettings} canEditInstance={isAdmin} />
        )}

        {activeTab === "keys" && <ApiKeysTab isAdmin={isAdmin} />}

        {activeTab === "tidal" && <TidalTab />}

        {activeTab === "apis" && <ApisTab />}

        {activeTab === "maintenance" && <MaintenanceTab />}
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
            <Button
              variant="outline"
              onClick={() => setShowResetConfirm(false)}
            >
              Cancel
            </Button>
            <Button onClick={handleReset}>Reset</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <FileBrowser
        isOpen={showFileBrowser}
        onClose={() => setShowFileBrowser(false)}
        onSelect={(p) =>
          setTempSettings((prev) => ({ ...prev, downloadPath: p }))
        }
        initialPath={tempSettings.downloadPath}
        title="Select Download Folder"
      />
    </div>
  );
}
