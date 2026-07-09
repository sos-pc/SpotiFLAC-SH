package audio

import "testing"

func TestConvertAudioRejectsUnsupportedOutputFormat(t *testing.T) {
	// OutputFormat is concatenated into an output directory and file
	// extension further down in ConvertAudio; an unvalidated value like
	// "../../../etc/cron.d" would let a caller write outside the input
	// file's directory. The format check must run before anything else so
	// this is safe to exercise without ffmpeg installed or real files.
	tests := []string{
		"../../../etc/cron.d",
		"flac/../../evil",
		"",
		"exe",
		"WAV",
	}
	for _, format := range tests {
		_, err := ConvertAudio(ConvertAudioRequest{
			InputFiles:   []string{"/music/track.flac"},
			OutputFormat: format,
		})
		if err == nil {
			t.Errorf("ConvertAudio with OutputFormat=%q: want error, got nil", format)
		}
	}
}

func TestConvertAudioAllowsSupportedOutputFormats(t *testing.T) {
	for _, format := range []string{"mp3", "MP3", "m4a"} {
		if !allowedConvertOutputFormats[lowerASCII(format)] {
			t.Errorf("expected format %q to be allowed", format)
		}
	}
}

// lowerASCII mirrors the strings.ToLower call ConvertAudio itself uses,
// duplicated here to avoid importing strings just for the test.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
