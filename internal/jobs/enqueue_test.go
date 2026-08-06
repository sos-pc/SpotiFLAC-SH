package jobs

import (
	"sync"
	"testing"
)

// TestEnqueueBatchConcurrentCallsDoNotDuplicate is the regression test for
// the EnqueueBatch TOCTOU race: two calls enqueuing the same track for the
// same watchlist at the same time (a watchlist's own scheduled sync firing
// alongside a manual "Sync" click, or two syncs racing after a restart)
// each read a snapshot of existing jobs before either inserts — without
// serialization, both snapshots miss the other call's insert and both
// proceed to create a job for the same track, leaving two duplicate
// Pending jobs for one track. Run with -race to also confirm jm.mu closes
// the underlying data race, not just the duplicate-count symptom.
func TestEnqueueBatchConcurrentCallsDoNotDuplicate(t *testing.T) {
	jm := newTestJobManager(t, false)

	const watchlistID = "wl-race-test"
	const spotifyID = "spotify:track:race-test"
	const concurrency = 10

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jm.EnqueueBatch(EnqueueBatchRequest{
				Tracks:      []JobTrack{{SpotifyID: spotifyID, TrackName: "Race Track", ArtistName: "Race Artist"}},
				WatchlistID: watchlistID,
			})
		}()
	}
	wg.Wait()

	jobs, err := jm.GetAllJobs()
	if err != nil {
		t.Fatalf("GetAllJobs: %v", err)
	}
	var matching int
	for _, j := range jobs {
		if j.SpotifyID == spotifyID && j.WatchlistID == watchlistID {
			matching++
		}
	}
	if matching != 1 {
		t.Errorf("got %d jobs for the same track+watchlist after %d concurrent EnqueueBatch calls, want exactly 1", matching, concurrency)
	}
}
