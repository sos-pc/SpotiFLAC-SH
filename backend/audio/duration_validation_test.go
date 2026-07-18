package audio

import "testing"

// This code DELETES files, so the cases that must not fire matter more than the
// ones that must. Calibrated against 13 real library files measured on
// 2026-07-18: 12 within 2% of the Spotify duration, worst case 13%, none above
// 25%.
func TestValidateTrackDuration(t *testing.T) {
	tests := []struct {
		name     string
		actual   int
		expected int
		reject   bool
		why      string
	}{
		// The failure this exists for.
		{"preview de 30s sur un morceau de 4min", 30, 240, true,
			"un proxy sans Premium sert un extrait et annonce un succès"},
		{"preview de 35s, pile sur le seuil", 35, 240, true, "seuil inclusif"},
		{"36s, juste au-dessus du seuil preview", 36, 240, true,
			"échappe à la règle preview, mais la règle d'écart le rattrape : 204s d'écart"},

		// Real measurements from the library: none of these may be deleted.
		{"She (mesuré)", 152, 151, false, "écart 1s"},
		{"Matrix (mesuré)", 241, 241, false, "identique"},
		{"Like a Blade of Grass (mesuré)", 300, 300, false, "identique"},
		{"One Very Important Thought (mesuré)", 85, 75, false,
			"écart 13% mais attendu < 90s : la règle d'écart ne s'applique pas"},

		// Boundaries of the mismatch rule.
		{"morceau court, gros écart relatif", 50, 80, false,
			"attendu < 90s : hors périmètre de la règle d'écart, volontairement"},
		{"écart de 20s sur 100s", 120, 100, false, "tolérance = max(15, 25) = 25s"},
		{"écart de 30s sur 100s", 130, 100, true, "au-delà de la tolérance de 25s"},
		{"écart de 16s sur 90s", 106, 90, false, "tolérance = max(15, 22) = 22s"},
		{"remaster plus long de 10%", 264, 240, false, "tolérance 60s, écart 24s"},

		// Refusing to judge.
		{"durée attendue inconnue", 30, 0, false, "sans référence, aucun verdict"},
		{"durée attendue négative", 30, -1, false, "idem"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDurations(tc.actual, tc.expected)
			if tc.reject && err == nil {
				t.Errorf("aurait dû rejeter (%s) : réel=%ds attendu=%ds", tc.why, tc.actual, tc.expected)
			}
			if !tc.reject && err != nil {
				t.Errorf("n'aurait pas dû rejeter (%s) : réel=%ds attendu=%ds — %v",
					tc.why, tc.actual, tc.expected, err)
			}
		})
	}
}

// A file that cannot be probed must never be deleted: an ffprobe failure is not
// evidence that the audio is wrong.
func TestValidateTrackDurationRefusesToJudgeWithoutData(t *testing.T) {
	if err := ValidateTrackDuration("", 240); err != nil {
		t.Errorf("chemin vide : %v", err)
	}
	if err := ValidateTrackDuration("/nonexistent/file.flac", 240); err != nil {
		t.Errorf("fichier illisible : doit être accepté, pas supprimé — %v", err)
	}
}
