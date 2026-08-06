package jobs

import "testing"

// tidalMayRunNatively decides whether to SKIP resolving stream URLs. A wrong
// "false" is the dangerous direction: the job then runs without a URL its native
// downloader needs, and fails for a reason that looks nothing like this function.
// So the false cases are enumerated as carefully as the true ones.
func TestTidalMayRunNatively(t *testing.T) {
	cases := []struct {
		name     string
		service  string
		order    string
		engineOn string // ENGINE_SERVICES
		want     bool
	}{
		// Tidal is the only remaining consumer of a pre-resolved ISRC.
		{"explicit tidal, native", "tidal", "", "", true},
		{"explicit amazon has no native path left", "amazon", "", "", false},
		{"explicit tidal, delegated", "tidal", "", "tidal", false},
		{"explicit amazon, delegated", "amazon", "", "amazon", false},

		// Qobuz never consumed them, delegated or not.
		{"explicit qobuz, native", "qobuz", "", "", false},
		{"explicit qobuz, delegated", "qobuz", "", "qobuz", false},

		// auto: one native URL consumer anywhere in the chain is enough.
		{"auto, tidal native in chain", "auto", "qobuz-tidal-deezer", "qobuz,deezer", true},
		{"auto, amazon in chain no longer forces resolution", "auto", "qobuz-amazon", "qobuz", false},
		{"auto, every consumer delegated", "auto", "tidal-qobuz-amazon-deezer", "tidal,qobuz,amazon,deezer", false},
		{"auto, chain has no URL consumer", "auto", "qobuz-deezer", "", false},

		// Conservative when the chain is not yet known: ExecuteDownload fills the
		// default in later, so skipping here would strip the ISRC for an unknown chain.
		{"auto with empty order", "auto", "", "tidal,qobuz,amazon,deezer", true},
		{"empty service behaves as auto", "", "", "tidal,qobuz,amazon,deezer", true},

		// Same tolerance the gate itself applies.
		{"case and spacing tolerated", "auto", " Tidal - qobuz ", "qobuz", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGINE_URL", "http://engine:8080")
			t.Setenv("ENGINE_SERVICES", tc.engineOn)

			got := tidalMayRunNatively(JobSettings{Service: tc.service, AutoOrder: tc.order})
			if got != tc.want {
				t.Errorf("tidalMayRunNatively(service=%q order=%q, ENGINE_SERVICES=%q) = %v, want %v",
					tc.service, tc.order, tc.engineOn, got, tc.want)
			}
		})
	}
}

// With no engine configured nothing is delegated, so the answer must match the
// behaviour that existed before the engine: resolve whenever a consumer could
// run. This is the regression guard for an install that never enables the engine
// at all.
func TestTidalMayRunNativelyWithoutEngine(t *testing.T) {
	t.Setenv("ENGINE_URL", "")
	t.Setenv("ENGINE_SERVICES", "tidal,qobuz,amazon,deezer")

	if !tidalMayRunNatively(JobSettings{Service: "tidal"}) {
		t.Error("tidal must still resolve an ISRC when ENGINE_URL is unset")
	}
	if !tidalMayRunNatively(JobSettings{Service: "auto", AutoOrder: "tidal-qobuz"}) {
		t.Error("auto chain containing tidal must resolve an ISRC when the engine is off")
	}
}
