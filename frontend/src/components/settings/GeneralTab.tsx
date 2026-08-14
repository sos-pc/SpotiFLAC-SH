import { type Dispatch, type SetStateAction } from "react";
import { InstanceScoped } from "./InstanceScoped";
import { Button } from "@/components/ui/button";
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
import { FolderOpen, ArrowRight } from "lucide-react";
import { themes } from "@/lib/themes";
import {
  DEFAULT_AUTO_ORDER,
  FONT_OPTIONS,
  type Settings as SettingsType,
  type FontFamily,
} from "@/lib/settings";
import {
  TidalIcon,
  QobuzIcon,
  AmazonIcon,
  DeezerIcon,
} from "@/components/settings/providerIcons";

interface GeneralTabProps {
  tempSettings: SettingsType;
  setTempSettings: Dispatch<SetStateAction<SettingsType>>;
  isDark: boolean;
  handleBrowseFolder: () => void;
  // downloadPath is the one instance-scoped setting on this tab: it decides
  // where files land in the shared library, and it used to double as the root
  // confining what its owner could browse.
  canEditInstance: boolean;
}

// GeneralTab — download path, theme/font, sound, and the download source +
// auto-fallback order. A controlled component: the parent owns tempSettings
// (shared with FilesTab and the Save/Reset chrome) and passes it plus a
// setter down. The quality handlers live here since only this tab uses them.
export function GeneralTab({
  tempSettings,
  setTempSettings,
  isDark,
  handleBrowseFolder,
  canEditInstance,
}: GeneralTabProps) {
  const handleTidalQualityChange = async (
    value: "LOSSLESS" | "HI_RES_LOSSLESS",
  ) => {
    setTempSettings((prev) => ({ ...prev, tidalQuality: value }));
  };
  const handleQobuzQualityChange = (value: "6" | "7" | "27") => {
    setTempSettings((prev) => ({ ...prev, qobuzQuality: value }));
  };
  const handleAutoQualityChange = async (value: "16" | "24") => {
    setTempSettings((prev) => ({ ...prev, autoQuality: value }));
  };
  return (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-4">
              <InstanceScoped canEdit={canEditInstance} what="The download path">
              <div className="space-y-2">
                <Label htmlFor="download-path">Download Path</Label>
                <div className="flex gap-2">
                  <InputWithContext
                    id="download-path"
                    value={tempSettings.downloadPath}
                    onChange={(e) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        downloadPath: e.target.value,
                      }))
                    }
                    placeholder="C:\Users\YourUsername\Music"
                  />
                  <Button
                    type="button"
                    onClick={handleBrowseFolder}
                    className="gap-1.5"
                  >
                    <FolderOpen className="h-4 w-4" />
                    Browse
                  </Button>
                </div>
              </div>
              </InstanceScoped>

              <div className="space-y-2">
                <Label htmlFor="theme-mode">Mode</Label>
                <Select
                  value={tempSettings.themeMode}
                  onValueChange={(value: "auto" | "light" | "dark") =>
                    setTempSettings((prev) => ({ ...prev, themeMode: value }))
                  }
                >
                  <SelectTrigger id="theme-mode">
                    <SelectValue placeholder="Select theme mode" />
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
                <Select
                  value={tempSettings.theme}
                  onValueChange={(value) =>
                    setTempSettings((prev) => ({ ...prev, theme: value }))
                  }
                >
                  <SelectTrigger id="theme">
                    <SelectValue placeholder="Select a theme" />
                  </SelectTrigger>
                  <SelectContent>
                    {themes.map((theme) => (
                      <SelectItem key={theme.name} value={theme.name}>
                        <span className="flex items-center gap-2">
                          <span
                            className="w-3 h-3 rounded-full border border-border"
                            style={{
                              backgroundColor: isDark
                                ? theme.cssVars.dark.primary
                                : theme.cssVars.light.primary,
                            }}
                          />
                          {theme.label}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="font">Font</Label>
                <Select
                  value={tempSettings.fontFamily}
                  onValueChange={(value: FontFamily) =>
                    setTempSettings((prev) => ({ ...prev, fontFamily: value }))
                  }
                >
                  <SelectTrigger id="font">
                    <SelectValue placeholder="Select a font" />
                  </SelectTrigger>
                  <SelectContent>
                    {FONT_OPTIONS.map((font) => (
                      <SelectItem key={font.value} value={font.value}>
                        <span style={{ fontFamily: font.fontFamily }}>
                          {font.label}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="flex items-center gap-3 pt-2">
                <Switch
                  id="sfx-enabled"
                  checked={tempSettings.sfxEnabled}
                  onCheckedChange={(checked) =>
                    setTempSettings((prev) => ({
                      ...prev,
                      sfxEnabled: checked,
                    }))
                  }
                />
                <Label
                  htmlFor="sfx-enabled"
                  className="cursor-pointer text-sm font-normal"
                >
                  Sound Effects
                </Label>
              </div>
            </div>

            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="downloader">Source</Label>
                <div className="flex gap-2 flex-wrap">
                  <Select
                    value={tempSettings.downloader}
                    onValueChange={(value: SettingsType["downloader"]) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        downloader: value,
                      }))
                    }
                  >
                    <SelectTrigger id="downloader" className="h-9 w-fit">
                      <SelectValue placeholder="Select a source" />
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
                      <SelectItem value="deezer">
                        <span className="flex items-center">
                          <DeezerIcon />
                          Deezer
                        </span>
                      </SelectItem>
                    </SelectContent>
                  </Select>

                  {tempSettings.downloader === "auto" && (
                    <>
                      <Select
                        value={tempSettings.autoOrder || DEFAULT_AUTO_ORDER}
                        onValueChange={(value: string) =>
                          setTempSettings((prev) => ({
                            ...prev,
                            autoOrder: value,
                          }))
                        }
                      >
                        <SelectTrigger className="h-9 w-fit min-w-[140px]">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="tidal-qobuz-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz-deezer-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-tidal-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-tidal-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz-amazon-tidal">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-amazon-tidal-qobuz">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                            </span>
                          </SelectItem>

                          <SelectItem value="tidal-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>

                          <SelectItem value="tidal-deezer">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-deezer">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-deezer">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <DeezerIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-tidal">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-qobuz">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="deezer-amazon">
                            <span className="flex items-center gap-1.5">
                              <DeezerIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-qobuz">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="tidal-amazon">
                            <span className="flex items-center gap-1.5">
                              <TidalIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-tidal">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="qobuz-amazon">
                            <span className="flex items-center gap-1.5">
                              <QobuzIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <AmazonIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-tidal">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <TidalIcon className="fill-current" />
                            </span>
                          </SelectItem>
                          <SelectItem value="amazon-qobuz">
                            <span className="flex items-center gap-1.5">
                              <AmazonIcon className="fill-current" />
                              <ArrowRight className="h-3 w-3 text-muted-foreground" />
                              <QobuzIcon className="fill-current" />
                            </span>
                          </SelectItem>
                        </SelectContent>
                      </Select>

                      <Select
                        value={tempSettings.autoQuality || "16"}
                        onValueChange={handleAutoQualityChange}
                      >
                        <SelectTrigger className="h-9 w-fit">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="16">16-bit/44.1kHz</SelectItem>
                          <SelectItem value="24">24-bit/48kHz</SelectItem>
                        </SelectContent>
                      </Select>
                    </>
                  )}

                  {tempSettings.downloader === "tidal" && (
                    <Select
                      value={tempSettings.tidalQuality}
                      onValueChange={handleTidalQualityChange}
                    >
                      <SelectTrigger className="h-9 w-fit">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="LOSSLESS">16-bit/44.1kHz</SelectItem>
                        <SelectItem value="HI_RES_LOSSLESS">
                          24-bit/48kHz
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  )}

                  {tempSettings.downloader === "qobuz" && (
                    <Select
                      value={tempSettings.qobuzQuality}
                      onValueChange={handleQobuzQualityChange}
                    >
                      <SelectTrigger className="h-9 w-fit">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="6">16-bit/44.1kHz</SelectItem>
                        <SelectItem value="27">
                          24-bit/48kHz - 192kHz
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  )}

                  {tempSettings.downloader === "amazon" && (
                    <div className="h-9 px-3 flex items-center text-sm font-medium border border-input rounded-md bg-muted/30 text-muted-foreground whitespace-nowrap cursor-default">
                      16-bit - 24-bit/44.1kHz - 192kHz
                    </div>
                  )}
                  {tempSettings.downloader === "deezer" && (
                    <div className="h-9 px-3 flex items-center text-sm font-medium border border-input rounded-md bg-muted/30 text-muted-foreground whitespace-nowrap cursor-default">
                      16-bit/44.1kHz
                    </div>
                  )}
                </div>

                {((tempSettings.downloader === "tidal" &&
                  tempSettings.tidalQuality === "HI_RES_LOSSLESS") ||
                  (tempSettings.downloader === "qobuz" &&
                    tempSettings.qobuzQuality === "27") ||
                  (tempSettings.downloader === "auto" &&
                    tempSettings.autoQuality === "24")) && (
                  <div className="flex items-center gap-3 pt-2">
                    <div className="flex items-center gap-3">
                      <Switch
                        id="allow-fallback"
                        checked={tempSettings.allowFallback}
                        onCheckedChange={(checked) =>
                          setTempSettings((prev) => ({
                            ...prev,
                            allowFallback: checked,
                          }))
                        }
                      />
                      <Label
                        htmlFor="allow-fallback"
                        className="text-sm font-normal cursor-pointer"
                      >
                        Allow Quality Fallback (16-bit)
                      </Label>
                    </div>
                  </div>
                )}
              </div>

              <div className="border-t pt-6" />

              <div className="space-y-4">
                <div className="flex items-center gap-3">
                  <Switch
                    id="embed-lyrics"
                    checked={tempSettings.embedLyrics}
                    onCheckedChange={(checked) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        embedLyrics: checked,
                      }))
                    }
                  />
                  <Label
                    htmlFor="embed-lyrics"
                    className="cursor-pointer text-sm font-normal"
                  >
                    Embed Lyrics
                  </Label>
                </div>
                <div className="flex items-center gap-3">
                  <Switch
                    id="embed-max-quality-cover"
                    checked={tempSettings.embedMaxQualityCover}
                    onCheckedChange={(checked) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        embedMaxQualityCover: checked,
                      }))
                    }
                  />
                  <Label
                    htmlFor="embed-max-quality-cover"
                    className="cursor-pointer text-sm font-normal"
                  >
                    Embed Max Quality Cover
                  </Label>
                </div>
                <div className="flex items-center gap-3">
                  <Switch
                    id="embed-genre"
                    checked={tempSettings.embedGenre}
                    onCheckedChange={(checked) =>
                      setTempSettings((prev) => ({
                        ...prev,
                        embedGenre: checked,
                      }))
                    }
                  />
                  <Label
                    htmlFor="embed-genre"
                    className="cursor-pointer text-sm font-normal"
                  >
                    Embed Genre
                  </Label>
                </div>
                {tempSettings.embedGenre && (
                  <div className="flex items-center gap-3">
                    <Switch
                      id="use-single-genre"
                      checked={tempSettings.useSingleGenre}
                      onCheckedChange={(checked) =>
                        setTempSettings((prev) => ({
                          ...prev,
                          useSingleGenre: checked,
                        }))
                      }
                    />
                    <Label
                      htmlFor="use-single-genre"
                      className="text-sm cursor-pointer font-normal"
                    >
                      Use Single Genre
                    </Label>
                  </div>
                )}
              </div>
            </div>
          </div>
  );
}
