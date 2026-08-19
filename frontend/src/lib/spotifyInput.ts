// What the user typed into the one search field, and what to do about it.
//
// The bar used to have two modes behind a toggle: paste a link and press Fetch,
// or flip a switch and type words. Two inputs, two pieces of state, and a
// button whose icon showed the mode you were NOT in. Clicking a search result
// silently flipped the toggle back, which is the tell that the mode was never
// a real distinction — the code already knew which one you meant.
//
// It still does. A Spotify link is recognisable on sight; anything else is
// words. These three predicates are that judgement, in one place, because both
// the bar and the page above it need the same answer: the bar to decide what
// to do on Enter, the page to decide whether a previously fetched album should
// stay on screen while you type something new.

/** A Spotify link or URI — something the fetch path can resolve. */
export function isSpotifyLink(text: string): boolean {
  const t = text.trim();
  if (!t) return false;
  return (
    /^spotify:/i.test(t) ||
    t.includes("spotify.com") ||
    t.includes("spotify.link")
  );
}

/** A URL, but not one of Spotify's — a paste that went wrong.
 *
 * Worth keeping separate from "words": pasting a YouTube link is a mistake the
 * user wants named, while typing `daft punk` is not. Merging the modes must not
 * turn a clear error message into a search that returns nothing.
 */
export function isForeignURL(text: string): boolean {
  const t = text.trim();
  if (!t) return false;
  const looksLikeURL = /^(https?:\/\/|www\.)/i.test(t) || /^spotify:/i.test(t);
  return looksLikeURL && !isSpotifyLink(t);
}

/** Words to search for, rather than a link to fetch or a bad paste to reject. */
export function isSearchTerms(text: string): boolean {
  const t = text.trim();
  return t.length > 0 && !isSpotifyLink(t) && !isForeignURL(t);
}
