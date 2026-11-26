package sqlite

import (
	"context"
	"testing"

	"libris/internal/domain"
)

// fillAudio writes what a build that sliced recordings stored: a source whose every segment
// is a window of audio, each with the file it was written to.
func fillAudio(t *testing.T, db *DB, id string, windows ...[2]float64) {
	t.Helper()
	ctx := context.Background()

	assets := make([]domain.Asset, 0, len(windows))
	for i := range windows {
		assets = append(assets, domain.Asset{
			ID:   id + "/clip-" + pad(i+1) + ".wav",
			Path: "/data/assets/" + id + "/clip-" + pad(i+1) + ".wav",
			MIME: "audio/wav", Bytes: 480044,
		})
	}
	if err := db.PutAssets(ctx, id, assets); err != nil {
		t.Fatalf("PutAssets: %v", err)
	}

	segs := make([]domain.Segment, 0, len(windows))
	for i, w := range windows {
		asset := assets[i]
		segs = append(segs, domain.Segment{
			ID: asset.ID, SourceID: id, DocID: id, Kind: domain.KindAudio,
			Ordinal: i, Asset: &asset,
			Span: domain.Provenance{T0: w[0], T1: w[1]},
		})
	}
	if err := db.PutSegments(ctx, id, segs); err != nil {
		t.Fatalf("PutSegments: %v", err)
	}
}

func pad(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func countSegments(t *testing.T, db *DB, sourceID string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(context.Background(),
		`SELECT count(*) FROM segments WHERE source_id = ?`, sourceID).Scan(&n); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	return n
}

// This is the only code in libris that removes a person's data without being asked, so the
// scope of it is the thing under test: the recordings an older build sliced go, everything
// else stays, and it happens once.
func TestPurgeLegacyAudioTakesOnlyRecordings(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "Fieldwork")
	b := notebook(t, db, "Papers")

	db.AddSource(ctx, a, source("oldrec", "standup.m4a"))
	db.AddSource(ctx, b, source("paper", "paper.pdf"))
	fillAudio(t, db, "oldrec", [2]float64{0, 30}, [2]float64{15, 45}, [2]float64{30, 60})
	fill(t, db, "paper", "/data/assets/paper/pages/page-0001.png")

	got, orphans, err := db.PurgeLegacyAudio(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("purged %d segments, want the three windows of the one recording", got)
	}
	if len(orphans) != 1 || orphans[0].ID != "oldrec" {
		t.Fatalf("orphans = %+v, want only the recording", orphans)
	}
	// The files travel with the answer, the same bargain DetachSource makes: app.Service
	// collects what this returns, so a clip nobody cited is unlinked and a citation that
	// still resolves is not.
	if len(orphans[0].Assets) != 3 {
		t.Errorf("orphan lists %d files, want the three clips", len(orphans[0].Assets))
	}
	if countSegments(t, db, "oldrec") != 0 {
		t.Error("the recording's segments are still there")
	}
	if countSegments(t, db, "paper") == 0 {
		t.Error("a PDF went with it")
	}
}

// Twice is the same as once. The marker is what makes that true, and without it the second
// start of this build would delete the clips this build is busy writing.
func TestPurgeLegacyAudioRunsOnce(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "Fieldwork")
	db.AddSource(ctx, a, source("oldrec", "standup.m4a"))
	fillAudio(t, db, "oldrec", [2]float64{0, 30})

	if n, _, err := db.PurgeLegacyAudio(ctx); err != nil || n != 1 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}

	// New clips, written by this build after the purge.
	db.AddSource(ctx, a, source("newrec", "lecture.mp3"))
	fillAudio(t, db, "newrec", [2]float64{0, 15}, [2]float64{7.5, 22.5})

	if n, orphans, err := db.PurgeLegacyAudio(ctx); err != nil || n != 0 || len(orphans) != 0 {
		t.Fatalf("second run: n=%d orphans=%d err=%v, want a no-op", n, len(orphans), err)
	}
	if countSegments(t, db, "newrec") != 2 {
		t.Error("this build's own clips were purged by the second run")
	}
}

// A source made of both a clip and a page cannot come from ingest, which writes one kind per
// source. If one exists anyway — a hand-edited database, an older build's bug — it is left
// whole rather than have its pages caught in a cascade aimed at something else.
func TestPurgeLegacyAudioLeavesAMixedSourceAlone(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "Odd")
	db.AddSource(ctx, a, source("mixed", "weird.mp3"))
	fillAudio(t, db, "mixed", [2]float64{0, 30})
	fill(t, db, "mixed", "/data/assets/mixed/pages/page-0001.png")

	if n, orphans, err := db.PurgeLegacyAudio(ctx); err != nil || n != 0 || len(orphans) != 0 {
		t.Fatalf("n=%d orphans=%d err=%v, want the mixed source untouched", n, len(orphans), err)
	}
	if countSegments(t, db, "mixed") != 2 {
		t.Errorf("mixed source kept %d segments, want both of them", countSegments(t, db, "mixed"))
	}
}

// Nothing to purge is the ordinary case, on every machine that never ran the old build.
func TestPurgeLegacyAudioOnAnEmptyLibrary(t *testing.T) {
	n, orphans, err := openDB(t).PurgeLegacyAudio(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(orphans) != 0 {
		t.Fatalf("n=%d orphans=%d, want nothing to do", n, len(orphans))
	}
}
