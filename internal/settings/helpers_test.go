package settings

import (
	"os"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	bolt "go.etcd.io/bbolt"
)

// newTestAuthManager builds an AuthManager on a throwaway BoltDB.
//
// The third copy of this helper, after internal/auth and the root package. Test
// scaffolding cannot cross a package boundary without being exported, and
// exporting it from internal/auth would put test-only construction in the API
// the binary links against.
func newTestAuthManager(t *testing.T) *auth.AuthManager {
	t.Helper()
	f, err := os.CreateTemp("", "spotiflac-test-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	database, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	am, err := auth.NewAuthManager(database)
	if err != nil {
		t.Fatalf("NewAuthManager: %v", err)
	}
	return am
}
