package backend

import "testing"

// engineHandles is the safety gate for the whole migration: until a provider is
// proven through the engine in prod, it must keep running its native Go path.
// A gate that opened by accident would silently reroute live downloads, so the
// closed cases matter more than the open one.
func TestEngineHandlesRequiresBothURLAndService(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		services string
		svc      string
		want     bool
	}{
		{"both unset — nothing delegated", "", "", "deezer", false},
		{"URL set but no service opted in", "http://engine:8080", "", "deezer", false},
		{"service opted in but no engine URL", "", "deezer", "deezer", false},
		{"opted in and reachable", "http://engine:8080", "deezer", "deezer", true},
		{"other provider stays native", "http://engine:8080", "deezer", "qobuz", false},
		{"multi-provider list", "http://engine:8080", "deezer,qobuz", "qobuz", true},
		{"whitespace and case are tolerated", "http://engine:8080", " Deezer , qobuz ", "deezer", true},
		{"substring must not match", "http://engine:8080", "deezer", "deez", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGINE_URL", tc.url)
			t.Setenv("ENGINE_SERVICES", tc.services)
			if got := EngineHandles(tc.svc); got != tc.want {
				t.Errorf("EngineHandles(%q) with URL=%q services=%q = %v, want %v",
					tc.svc, tc.url, tc.services, got, tc.want)
			}
		})
	}
}

func TestEngineStagingDirDefaultsAndOverrides(t *testing.T) {
	t.Setenv("ENGINE_STAGING_DIR", "")
	if got := EngineStagingDir(); got != "/staging" {
		t.Errorf("default staging dir = %q, want /staging", got)
	}
	t.Setenv("ENGINE_STAGING_DIR", "/mnt/scratch")
	if got := EngineStagingDir(); got != "/mnt/scratch" {
		t.Errorf("override staging dir = %q, want /mnt/scratch", got)
	}
}
