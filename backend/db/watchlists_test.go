package db

import (
	"context"
	"fmt"
	"testing"
)

func TestSetWatchlistTracksBasicDiff(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	for _, id := range []string{"t1", "t2", "t3"} {
		if err := UpsertTrackStub(ctx, database, id); err != nil {
			t.Fatalf("UpsertTrackStub(%s): %v", id, err)
		}
	}

	added, removed, err := SetWatchlistTracks(ctx, database, "watch-1", []string{"t1", "t2"})
	if err != nil {
		t.Fatalf("SetWatchlistTracks (initial): %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("initial removed = %v, want empty", removed)
	}
	if len(added) != 2 {
		t.Errorf("initial added = %v, want 2 entries", added)
	}

	ids, err := ListWatchlistTrackIDs(ctx, database, "watch-1")
	if err != nil {
		t.Fatalf("ListWatchlistTrackIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ListWatchlistTrackIDs = %v, want 2 entries", ids)
	}

	// t2 stays, t1 drops out, t3 joins.
	added, removed, err = SetWatchlistTracks(ctx, database, "watch-1", []string{"t2", "t3"})
	if err != nil {
		t.Fatalf("SetWatchlistTracks (update): %v", err)
	}
	if len(added) != 1 || added[0] != "t3" {
		t.Errorf("added = %v, want [t3]", added)
	}
	if len(removed) != 1 || removed[0] != "t1" {
		t.Errorf("removed = %v, want [t1]", removed)
	}

	ids, err = ListWatchlistTrackIDs(ctx, database, "watch-1")
	if err != nil {
		t.Fatalf("ListWatchlistTrackIDs: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if len(got) != 2 || !got["t2"] || !got["t3"] {
		t.Errorf("final track set = %v, want {t2, t3}", ids)
	}
}

// TestSetWatchlistTracksLargeBatch is the regression test for the
// SQLITE_BUSY contention observed in production on a 2500+ track
// aggregator watchlist: SetWatchlistTracks used to insert one row per
// track inside a single transaction, holding the write lock for the
// whole operation. This exercises the batched-insert path across
// multiple batches (setWatchlistTracksBatchSize=500) and confirms every
// row still lands correctly.
func TestSetWatchlistTracksLargeBatch(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	const n = 1200 // > 2 full batches of 500
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("track-%04d", i)
		if err := UpsertTrackStub(ctx, database, ids[i]); err != nil {
			t.Fatalf("UpsertTrackStub(%s): %v", ids[i], err)
		}
	}

	added, removed, err := SetWatchlistTracks(ctx, database, "watch-big", ids)
	if err != nil {
		t.Fatalf("SetWatchlistTracks: %v", err)
	}
	if len(added) != n {
		t.Fatalf("added = %d entries, want %d", len(added), n)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %d entries, want 0", len(removed))
	}

	got, err := ListWatchlistTrackIDs(ctx, database, "watch-big")
	if err != nil {
		t.Fatalf("ListWatchlistTrackIDs: %v", err)
	}
	if len(got) != n {
		t.Fatalf("ListWatchlistTrackIDs returned %d rows, want %d — a batch was dropped", len(got), n)
	}
	for i, id := range got {
		if id != ids[i] {
			t.Fatalf("position %d = %q, want %q — order not preserved across batches", i, id, ids[i])
		}
	}
}

func TestIsTrackInOtherWatchlists(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	for _, id := range []string{"shared", "only-a"} {
		if err := UpsertTrackStub(ctx, database, id); err != nil {
			t.Fatalf("UpsertTrackStub(%s): %v", id, err)
		}
	}
	if _, _, err := SetWatchlistTracks(ctx, database, "watch-a", []string{"shared", "only-a"}); err != nil {
		t.Fatalf("SetWatchlistTracks(watch-a): %v", err)
	}
	if _, _, err := SetWatchlistTracks(ctx, database, "watch-b", []string{"shared"}); err != nil {
		t.Fatalf("SetWatchlistTracks(watch-b): %v", err)
	}

	inOther, err := IsTrackInOtherWatchlists(ctx, database, "shared", "watch-a")
	if err != nil {
		t.Fatalf("IsTrackInOtherWatchlists: %v", err)
	}
	if !inOther {
		t.Error("shared track should be reported as present in watch-b too")
	}

	inOther, err = IsTrackInOtherWatchlists(ctx, database, "only-a", "watch-a")
	if err != nil {
		t.Fatalf("IsTrackInOtherWatchlists: %v", err)
	}
	if inOther {
		t.Error("only-a track should not be reported as present in any other watchlist")
	}
}
