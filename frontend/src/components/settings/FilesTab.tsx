import { type Dispatch, type SetStateAction } from "react";
import { InstanceScoped } from "./InstanceScoped";
import { InputWithContext } from "@/components/ui/input-with-context";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Info } from "lucide-react";
import {
  FOLDER_PRESETS,
  FILENAME_PRESETS,
  TEMPLATE_VARIABLES,
  type Settings as SettingsType,
  type FolderPreset,
  type FilenamePreset,
} from "@/lib/settings";

interface FilesTabProps {
  tempSettings: SettingsType;
  setTempSettings: Dispatch<SetStateAction<SettingsType>>;
  // Every setting on this tab is instance-scoped — folder structure, filename
  // template, the Jellyfin path — so the whole thing is gated rather than
  // individual fields. The two preset selectors are not read by the backend at
  // all, but they write the templates that are, so they follow.
  canEditInstance: boolean;
}

// FilesTab — folder-structure and filename-template configuration. Controlled
// component sharing tempSettings with the parent (see GeneralTab).
export function FilesTab({ tempSettings, setTempSettings, canEditInstance }: FilesTabProps) {
  return (
    <InstanceScoped canEdit={canEditInstance} what="Folder and filename settings">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-4">
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <Label className="text-sm">Folder Structure</Label>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Info className="h-3.5 w-3.5 text-muted-foreground cursor-help" />
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
                  <Select
                    value={tempSettings.folderPreset}
                    onValueChange={(value: FolderPreset) => {
                      const preset = FOLDER_PRESETS[value];
                      setTempSettings((prev) => ({
                        ...prev,
                        folderPreset: value,
                        folderTemplate:
                          value === "custom"
                            ? prev.folderTemplate || preset.template
                            : preset.template,
                      }));
                    }}
                  >
                    <SelectTrigger className="h-9 w-fit">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {Object.entries(FOLDER_PRESETS).map(
                        ([key, { label }]) => (
                          <SelectItem key={key} value={key}>
                            {label}
                          </SelectItem>
                        ),
                      )}
                    </SelectContent>
                  </Select>
                  {tempSettings.folderPreset === "custom" && (
                    <InputWithContext
                      value={tempSettings.folderTemplate}
                      onChange={(e) =>
                        setTempSettings((prev) => ({
                          ...prev,
                          folderTemplate: e.target.value,
                        }))
                      }
                      placeholder="{artist}/{album}"
                      className="h-9 text-sm flex-1"
                    />
                  )}
                </div>
                {tempSettings.folderTemplate && (
                  <p className="text-xs text-muted-foreground">
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
                  </p>
                )}
              </div>

              <div className="flex items-center gap-3">
                <Switch
                  id="create-playlist-folder"
                  checked={tempSettings.createPlaylistFolder}
                  onCheckedChange={(checked) =>
                    setTempSettings((prev) => ({
                      ...prev,
                      createPlaylistFolder: checked,
                    }))
                  }
                />
                <Label
                  htmlFor="create-playlist-folder"
                  className="text-sm cursor-pointer font-normal"
                >
                  Playlist Folder
                </Label>
              </div>

              <div className="flex items-center gap-3">
                <Switch
                  id="create-m3u8-file"
                  checked={tempSettings.createM3u8File}
                  onCheckedChange={(checked) =>
                    setTempSettings((prev) => ({
                      ...prev,
                      createM3u8File: checked,
                    }))
                  }
                />
                <Label
                  htmlFor="create-m3u8-file"
                  className="text-sm cursor-pointer font-normal"
                >
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
                      onChange={(e) =>
                        setTempSettings((prev) => ({
                          ...prev,
                          jellyfinMusicPath: e.target.checked
                            ? "/Multimedia/Musique/Spotiflac"
                            : "",
                        }))
                      }
                      className="rounded"
                    />
                    <Label
                      htmlFor="jellyfin-m3u8"
                      className="text-sm cursor-pointer font-normal"
                    >
                      Jellyfin compatible paths
                    </Label>
                  </div>
                  {!!tempSettings.jellyfinMusicPath && (
                    <div className="flex flex-col gap-1">
                      <Label className="text-xs text-muted-foreground">
                        Jellyfin music library path
                      </Label>
                      <input
                        type="text"
                        value={tempSettings.jellyfinMusicPath}
                        onChange={(e) =>
                          setTempSettings((prev) => ({
                            ...prev,
                            jellyfinMusicPath: e.target.value,
                          }))
                        }
                        placeholder="/Multimedia/Musique/Spotiflac"
                        className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm font-mono"
                      />
                      <p className="text-xs text-muted-foreground">
                        Path as seen by Jellyfin (replaces /home/nonroot/Music
                        in M3U8 files)
                      </p>
                    </div>
                  )}
                </div>
              )}

              <div className="flex items-center gap-3">
                <Switch
                  id="use-first-artist-only"
                  checked={tempSettings.useFirstArtistOnly}
                  onCheckedChange={(checked) =>
                    setTempSettings((prev) => ({
                      ...prev,
                      useFirstArtistOnly: checked,
                    }))
                  }
                />
                <Label
                  htmlFor="use-first-artist-only"
                  className="text-sm cursor-pointer font-normal"
                >
                  Use First Artist Only
                </Label>
              </div>
            </div>

            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <Label className="text-sm">Filename Format</Label>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Info className="h-3.5 w-3.5 text-muted-foreground cursor-help" />
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
                <Select
                  value={tempSettings.filenamePreset}
                  onValueChange={(value: FilenamePreset) => {
                    const preset = FILENAME_PRESETS[value];
                    setTempSettings((prev) => ({
                      ...prev,
                      filenamePreset: value,
                      filenameTemplate:
                        value === "custom"
                          ? prev.filenameTemplate || preset.template
                          : preset.template,
                    }));
                  }}
                >
                  <SelectTrigger className="h-9 w-fit">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.entries(FILENAME_PRESETS).map(
                      ([key, { label }]) => (
                        <SelectItem key={key} value={key}>
                          {label}
                        </SelectItem>
                      ),
                    )}
                  </SelectContent>
                </Select>
                {tempSettings.filenamePreset === "custom" && (
                  <InputWithContext
                    value={tempSettings.filenameTemplate}
                    onChange={(e) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        filenameTemplate: e.target.value,
                      }))
                    }
                    placeholder="{track}. {title}"
                    className="h-9 text-sm flex-1"
                  />
                )}
              </div>
              {tempSettings.filenameTemplate && (
                <p className="text-xs text-muted-foreground">
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
                </p>
              )}
            </div>
          </div>
    </InstanceScoped>
  );
}
