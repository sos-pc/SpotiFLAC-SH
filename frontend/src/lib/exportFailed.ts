import { ExportFailedDownloads } from "./rpc";

// Saving a blob is fiddly enough to be worth having one implementation of.
function saveBlob(contents: string, filename: string, type: string) {
  const url = URL.createObjectURL(new Blob([contents], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  // In the document, not detached: a synthetic click on an anchor outside the
  // document is not reliably actioned across browsers.
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Not revoked on the line after click(), which is where it was. click() only
  // *starts* the download; revoking in the same task can pull the blob out from
  // under it. One turn of the event loop is enough for the browser to have
  // taken its own reference.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

// Two pages offer an "Export Failed" button — the download queue and the debug
// log — and each used to interpret the response its own way.
//
// The debug page's reading was left over from the desktop build: it tested the
// reply for "Successfully" and "Export cancelled", which are what a native
// save-file dialog answered. The web backend has never returned either. So that
// button downloaded nothing, ever, and on the one path where there was actually
// something to export it printed the raw CSV into a toast.
//
// One caller, one behaviour.
// Two flat fields rather than a discriminated union on `saved`: this project
// compiles with strict and strictNullChecks off (tsconfig.app.json), and
// without them a union does not narrow in the caller's else branch — `message`
// would be a type error at every call site.
export interface ExportFailedResult {
  saved: boolean;
  // Why nothing was saved. Empty when it was.
  message: string;
}

export async function exportFailedDownloadsToFile(): Promise<ExportFailedResult> {
  const { csv, message } = await ExportFailedDownloads();
  if (!csv) {
    return { saved: false, message: message || "Nothing to export" };
  }
  saveBlob(csv, "failed_downloads.csv", "text/csv;charset=utf-8");
  return { saved: true, message: "" };
}
