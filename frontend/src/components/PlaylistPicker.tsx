import { useCallback, useMemo, useState } from "react";
import { Search, Link2, User, Loader2, Check } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import {
  AddWatchlistsBatch,
  GetProfilePlaylists,
  SearchSpotifyProfiles,
  type PickerPlaylist,
  type SpotifyProfile,
} from "@/lib/rpc";

// Adding a playlist to the watchlist used to mean pasting one URL, confirming,
// and repeating. Twelve times for twelve playlists. This is the same operation
// with the list in front of you.
//
// Three sources were designed (see docs/multi-user-readiness-plan.md §8): your
// own playlists over OAuth, someone's public profile, and a URL. Only the last
// two exist — OAuth needs a Spotify application declared by the operator, which
// has not happened — so the first is shown and explained rather than hidden.
// Hiding it would make the picker look complete and leave the operator with no
// hint that connecting an account is what unlocks private playlists.

type Source = "mine" | "profile" | "url";
type OwnerFilter = "all" | "owned" | "followed";

export function PlaylistPicker({
  isOpen,
  onClose,
  onAdded,
  watchedURLs,
}: {
  isOpen: boolean;
  onClose: () => void;
  onAdded: () => void;
  // Playlist URLs this user already watches. The server refuses duplicates
  // anyway, but offering them as available and then refusing the click is
  // lying to the screen.
  watchedURLs: Set<string>;
}) {
  const [source, setSource] = useState<Source>("profile");

  // ── Profile search ────────────────────────────────────────────────────────
  const [query, setQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [profiles, setProfiles] = useState<SpotifyProfile[] | null>(null);
  const [profile, setProfile] = useState<SpotifyProfile | null>(null);

  const runSearch = useCallback(async () => {
    const q = query.trim();
    if (!q) return;
    setSearching(true);
    setProfiles(null);
    try {
      setProfiles(await SearchSpotifyProfiles(q));
    } catch (e) {
      toast.error(`Could not search profiles: ${e}`);
    } finally {
      setSearching(false);
    }
  }, [query]);

  // ── The chosen profile's playlists ────────────────────────────────────────
  const [loading, setLoading] = useState(false);
  const [playlists, setPlaylists] = useState<PickerPlaylist[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState<OwnerFilter>("all");
  const [needle, setNeedle] = useState("");

  const openProfile = useCallback(async (p: SpotifyProfile) => {
    setProfile(p);
    setLoading(true);
    setPlaylists([]);
    setSelected(new Set());
    try {
      setPlaylists(await GetProfilePlaylists(p.id));
    } catch (e) {
      toast.error(`Could not read that profile: ${e}`);
      setProfile(null);
    } finally {
      setLoading(false);
    }
  }, []);

  const playlistURL = (p: PickerPlaylist) =>
    `https://open.spotify.com/playlist/${p.id}`;

  const visible = useMemo(() => {
    const n = needle.trim().toLowerCase();
    return playlists.filter((p) => {
      if (filter === "owned" && !p.owned) return false;
      if (filter === "followed" && p.owned) return false;
      if (n && !p.name.toLowerCase().includes(n)) return false;
      return true;
    });
  }, [playlists, filter, needle]);

  const toggle = (p: PickerPlaylist) =>
    setSelected((prev) => {
      const next = new Set(prev);
      const url = playlistURL(p);
      if (next.has(url)) next.delete(url);
      else next.add(url);
      return next;
    });

  const selectableVisible = visible.filter(
    (p) => !watchedURLs.has(playlistURL(p)),
  );
  const allVisibleSelected =
    selectableVisible.length > 0 &&
    selectableVisible.every((p) => selected.has(playlistURL(p)));

  const toggleAllVisible = () =>
    setSelected((prev) => {
      const next = new Set(prev);
      for (const p of selectableVisible) {
        const url = playlistURL(p);
        if (allVisibleSelected) next.delete(url);
        else next.add(url);
      }
      return next;
    });

  // ── URL source ────────────────────────────────────────────────────────────
  const [url, setUrl] = useState("");

  const [adding, setAdding] = useState(false);
  const submit = useCallback(
    async (urls: string[]) => {
      if (urls.length === 0) return;
      setAdding(true);
      try {
        const res = await AddWatchlistsBatch({
          spotify_urls: urls,
          interval_hours: 24,
          sync_deletions: false,
        });
        // Every outcome is reported, not just the count: "3 of 12 failed" with
        // no names leaves comparing the list by hand as the only recovery.
        if (res.added > 0) {
          toast.success(
            `Now watching ${res.added} playlist${res.added === 1 ? "" : "s"}`,
          );
        }
        if (res.already_watched > 0) {
          toast.info(`${res.already_watched} were already watched`);
        }
        for (const o of res.outcomes) {
          if (o.status === "failed") {
            toast.error(`${o.name || o.spotify_url}: ${o.error}`);
          }
        }
        setSelected(new Set());
        setUrl("");
        onAdded();
        if (res.failed === 0) onClose();
      } catch (e) {
        toast.error(`Could not add: ${e}`);
      } finally {
        setAdding(false);
      }
    },
    [onAdded, onClose],
  );

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="max-w-[720px] w-[95vw] max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>Add playlists to watch</DialogTitle>
          <DialogDescription>
            Watched playlists are re-checked regularly and new tracks are
            downloaded automatically.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-2" role="tablist" aria-label="Playlist source">
          <Button
            role="tab"
            aria-selected={source === "mine"}
            variant={source === "mine" ? "secondary" : "ghost"}
            size="sm"
            className="gap-1.5"
            onClick={() => setSource("mine")}
          >
            <User className="h-3.5 w-3.5" />
            My playlists
          </Button>
          <Button
            role="tab"
            aria-selected={source === "profile"}
            variant={source === "profile" ? "secondary" : "ghost"}
            size="sm"
            className="gap-1.5"
            onClick={() => setSource("profile")}
          >
            <Search className="h-3.5 w-3.5" />
            A profile
          </Button>
          <Button
            role="tab"
            aria-selected={source === "url"}
            variant={source === "url" ? "secondary" : "ghost"}
            size="sm"
            className="gap-1.5"
            onClick={() => setSource("url")}
          >
            <Link2 className="h-3.5 w-3.5" />
            A link
          </Button>
        </div>

        <div className="flex-1 overflow-y-auto min-h-[280px]">
          {source === "mine" && (
            <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
              <p className="mb-1 font-medium text-foreground">
                Connecting your Spotify account is not available yet
              </p>
              <p>
                It is what would let you pick from your own playlists, including
                private and collaborative ones. Until then, a public profile or
                a link works — and an administrator has to register a Spotify
                application before any account can be connected.
              </p>
            </div>
          )}

          {source === "profile" && !profile && (
            <div className="space-y-3">
              <form
                className="flex gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void runSearch();
                }}
              >
                <Label htmlFor="profile-q" className="sr-only">
                  Spotify profile name
                </Label>
                <Input
                  id="profile-q"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search a Spotify profile by name"
                />
                <Button type="submit" disabled={searching || !query.trim()}>
                  {searching ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    "Search"
                  )}
                </Button>
              </form>

              {profiles?.length === 0 && (
                <p className="px-1 text-sm text-muted-foreground">
                  No profile found. Display names are not unique on Spotify, so
                  a common one may need the exact spelling.
                </p>
              )}

              <ul className="space-y-1">
                {profiles?.map((p) => (
                  <li key={p.id}>
                    <button
                      type="button"
                      onClick={() => void openProfile(p)}
                      className="flex w-full items-center gap-3 rounded-md p-2 text-left hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {p.image_url ? (
                        // The id, not just the name: ten accounts can be called
                        // "Marc" and the avatar is the only thing telling them
                        // apart, so alt text repeating the name conveys nothing.
                        <img
                          src={p.image_url}
                          alt={`Avatar of ${p.display_name} (${p.id})`}
                          className="h-8 w-8 rounded-full object-cover"
                        />
                      ) : (
                        <span
                          aria-hidden="true"
                          className="flex h-8 w-8 items-center justify-center rounded-full bg-muted text-xs"
                        >
                          {p.display_name.slice(0, 2).toUpperCase()}
                        </span>
                      )}
                      <span className="min-w-0">
                        <span className="block truncate text-sm font-medium">
                          {p.display_name}
                        </span>
                        <span className="block truncate text-xs text-muted-foreground">
                          {p.id}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {source === "profile" && profile && (
            <div className="space-y-3">
              <div className="flex items-center justify-between gap-2">
                <p className="text-sm">
                  <span className="font-medium">{profile.display_name}</span>
                  <span className="text-muted-foreground">
                    {" "}
                    — {playlists.length} public playlist
                    {playlists.length === 1 ? "" : "s"}
                  </span>
                </p>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setProfile(null);
                    setPlaylists([]);
                    setSelected(new Set());
                  }}
                >
                  Another profile
                </Button>
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <Label htmlFor="playlist-filter" className="sr-only">
                  Filter playlists by name
                </Label>
                <Input
                  id="playlist-filter"
                  value={needle}
                  onChange={(e) => setNeedle(e.target.value)}
                  placeholder="Filter by name"
                  className="h-8 flex-1 min-w-[140px] text-sm"
                />
                {(["all", "owned", "followed"] as OwnerFilter[]).map((f) => (
                  <Button
                    key={f}
                    type="button"
                    size="sm"
                    variant={filter === f ? "secondary" : "ghost"}
                    aria-pressed={filter === f}
                    onClick={() => setFilter(f)}
                    className="h-8 text-xs capitalize"
                  >
                    {f === "owned" ? "Theirs" : f === "followed" ? "Followed" : "All"}
                  </Button>
                ))}
              </div>

              {loading ? (
                <p className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Reading the profile…
                </p>
              ) : (
                <>
                  {selectableVisible.length > 0 && (
                    <div className="flex items-center gap-2 border-b pb-2">
                      <Checkbox
                        id="select-all"
                        checked={allVisibleSelected}
                        onCheckedChange={toggleAllVisible}
                      />
                      <Label htmlFor="select-all" className="text-xs">
                        Select all shown
                      </Label>
                    </div>
                  )}
                  <ul className="space-y-1">
                    {visible.map((p) => {
                      const url = playlistURL(p);
                      const already = watchedURLs.has(url);
                      return (
                        <li
                          key={p.uri}
                          className="flex items-center gap-3 rounded-md p-1.5 hover:bg-muted/40"
                        >
                          <Checkbox
                            id={`pl-${p.id}`}
                            checked={already || selected.has(url)}
                            disabled={already}
                            onCheckedChange={() => toggle(p)}
                          />
                          <Label
                            htmlFor={`pl-${p.id}`}
                            className="flex min-w-0 flex-1 items-center gap-2 font-normal"
                          >
                            <span className="min-w-0 flex-1 truncate text-sm">
                              {p.name}
                            </span>
                            {!p.owned && (
                              <span className="shrink-0 text-xs text-muted-foreground">
                                followed
                              </span>
                            )}
                            {already && (
                              <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
                                <Check className="h-3 w-3" />
                                already watched
                              </span>
                            )}
                          </Label>
                        </li>
                      );
                    })}
                  </ul>
                  {visible.length === 0 && playlists.length > 0 && (
                    <p className="p-4 text-sm text-muted-foreground">
                      Nothing matches this filter.
                    </p>
                  )}
                  {playlists.length === 0 && (
                    <p className="p-4 text-sm text-muted-foreground">
                      This profile shares no playlists publicly. Private ones are
                      only reachable by connecting the account.
                    </p>
                  )}
                </>
              )}
            </div>
          )}

          {source === "url" && (
            <form
              className="flex gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                void submit([url.trim()]);
              }}
            >
              <Label htmlFor="playlist-url" className="sr-only">
                Spotify playlist link
              </Label>
              <Input
                id="playlist-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://open.spotify.com/playlist/…"
              />
              <Button
                type="submit"
                disabled={adding || !url.includes("spotify.com")}
              >
                Add
              </Button>
            </form>
          )}
        </div>

        <div className="flex items-center justify-between gap-3 border-t pt-3">
          {/* Announced, because a count that only changes visually does not
              exist for anyone using a screen reader. */}
          <p aria-live="polite" className="text-sm text-muted-foreground">
            {selected.size === 0
              ? "Nothing selected"
              : `${selected.size} playlist${selected.size === 1 ? "" : "s"} selected`}
          </p>
          <Button
            disabled={adding || selected.size === 0}
            onClick={() => void submit([...selected])}
          >
            {adding
              ? "Adding…"
              : `Watch ${selected.size || ""} playlist${selected.size === 1 ? "" : "s"}`.trim()}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
