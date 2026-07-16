package util

// Hardening flags for every ffmpeg/ffprobe invocation that parses a file we did
// not author.
//
// The threat is concrete, not theoretical. The bytes ffmpeg decodes here are
// chosen by third parties: the community Tidal/Qobuz/Deezer proxies return a
// manifest, and *they* pick the URLs the audio is fetched from (see
// docs/third-party-layer-status.md — that layer is unofficial, unmaintained,
// and visibly eroding). A media decoder is historically among the most
// exploited classes of code there is, and an RCE inside this container reaches
// the JWT secret (forging admin tokens indefinitely), the Tidal refresh token,
// and the music library bind-mount. See docs/ffmpeg-runtime-regression.md §4.
//
// These flags do not stop an exploit — they remove the two cheapest things a
// crafted file can ask for:
//
//   - `-protocol_whitelist file` — a container format can reference external
//     resources (mov/mp4 `dref` boxes, playlist/concat entries). Without this,
//     a crafted file makes ffmpeg open http/tcp URLs of the attacker's choosing:
//     SSRF from inside the container, and the exfiltration leg of any local-file
//     read. Restricting it to `file` keeps our own local input/output working
//     and cuts the network leg entirely — we never legitimately hand ffmpeg a
//     URL (verified: every call site passes a local path).
//   - `-nostdin` — stops ffmpeg consuming/blocking on the parent's stdin.
//
// Split in two because they are not interchangeable: `-protocol_whitelist` is
// an avformat option both binaries accept, while `-nostdin` is ffmpeg-only —
// passing it to ffprobe would make it exit non-zero and silently break every
// tag read. The CI smoke test runs a real conversion and a real probe with
// these exact flags, so a wrong assumption here fails the build instead of
// production (which is precisely how the regression these live alongside got
// out).

// FFmpegHardeningArgs are prepended to ffmpeg invocations, before -i.
func FFmpegHardeningArgs() []string {
	return []string{"-nostdin", "-protocol_whitelist", "file"}
}

// FFprobeHardeningArgs are prepended to ffprobe invocations, before the input.
// No -nostdin: ffprobe does not accept it.
func FFprobeHardeningArgs() []string {
	return []string{"-protocol_whitelist", "file"}
}
