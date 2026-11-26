package sqlite

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"libris/internal/domain"
)

// Benchmarks for the vector scan. They are kept because the two numbers that decide
// how a scan is split — scanRowsPerShard and searchShardCap — were set by running
// these, and a comment claiming "set by measurement" is only worth the measurement.
//
//	go test ./internal/store/sqlite/ -run '^$' -bench Scan -benchtime 3x
//
// benchDim is 768 because that is what the model on the machine that ran this
// reports (Embeddinggemma-2+tp1, 768 dimensions), so the cost per row is the cost a
// real search pays rather than a round number's approximation of it.

const (
	benchDim  = 768
	benchRows = 33_000 // enough for eight shards at the real scanRowsPerShard
	benchTop  = 40
)

// benchDB writes a corpus of rows chunks with random vectors, in one notebook whose
// source is ready. Random rather than repeated vectors: a benchmark wants the scan
// to do the work a corpus of real documents does, not to be handed a tie band.
func benchDB(b *testing.B, rows int) (*DB, string) {
	b.Helper()
	ctx := context.Background()

	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })

	made, err := db.CreateNotebook(ctx, "bench")
	if err != nil {
		b.Fatal(err)
	}
	nb := made.ID
	if _, _, err := db.AddSource(ctx, nb, domain.Source{
		ID: "bench", Name: "bench.pdf", Kind: domain.SourcePDF, SHA256: "bench",
		Status: domain.StatusProcessing,
	}); err != nil {
		b.Fatal(err)
	}

	rng := rand.NewPCG(1, 2)
	random := rand.New(rng)

	segs := make([]domain.Segment, 0, rows)
	embs := make([]domain.Embedding, 0, rows)
	for i := range rows {
		id := fmt.Sprintf("bench:%06d", i)
		segs = append(segs, domain.Segment{
			ID: id, SourceID: "bench", DocID: "bench", Kind: domain.KindText,
			Ordinal: i, Text: fmt.Sprintf("chunk %d of the benchmark corpus, long enough to be "+
				"worth carrying through a driver and short enough not to dominate the row.", i),
			Span: domain.Provenance{Page: 1, CharStart: i, CharEnd: i + 1},
		})
		vec := make([]float32, benchDim)
		for d := range vec {
			vec[d] = float32(random.NormFloat64())
		}
		embs = append(embs, domain.Embedding{SegmentID: id, Model: "bench+tp1", Dim: benchDim, Vector: vec})
	}

	if err := db.PutSegments(ctx, "bench", segs); err != nil {
		b.Fatal(err)
	}
	if err := db.PutEmbeddings(ctx, embs); err != nil {
		b.Fatal(err)
	}
	if err := db.SetSourceStatus(ctx, "bench", domain.StatusReady, "", nil); err != nil {
		b.Fatal(err)
	}
	return db, nb
}

// BenchmarkScanShards is the whole question the fan-out is meant to answer: how much
// of a scan is left when it is split across more of the machine.
func BenchmarkScanShards(b *testing.B) {
	ctx := context.Background()
	db, nb := benchDB(b, benchRows)

	query := make([]float32, benchDim)
	rng := rand.New(rand.NewPCG(3, 4))
	for d := range query {
		query[d] = float32(rng.NormFloat64())
	}

	for _, shards := range []int{1, 2, 3, 4, 6, 8, 12} {
		db.shards = shards
		b.Run(fmt.Sprintf("shards=%02d", shards), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				hits, err := db.SearchSegments(ctx, nb, query, "bench+tp1", benchTop)
				if err != nil {
					b.Fatal(err)
				}
				if len(hits) != benchTop {
					b.Fatalf("%d hits, want %d", len(hits), benchTop)
				}
			}
		})
	}
}

// BenchmarkScanWithText is the scan as it was: one query carrying the text of every
// candidate, one thread. The gap between it and shards=1 is what dropping the text
// column bought; the gap between shards=1 and the best shard count is what the
// fan-out buys on top.
func BenchmarkScanWithText(b *testing.B) {
	db, nb := benchDB(b, benchRows)

	query := make([]float32, benchDim)
	rng := rand.New(rand.NewPCG(3, 4))
	for d := range query {
		query[d] = float32(rng.NormFloat64())
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		hits := legacyScan(b, db, nb, query, "bench+tp1", benchTop)
		if len(hits) != benchTop {
			b.Fatalf("%d hits, want %d", len(hits), benchTop)
		}
	}
}

// BenchmarkScanSplitThreshold asks where a split starts paying, on a corpus small
// enough that a shard is a handful of chunks. scanRowsPerShard has to sit above the
// point where splitting is still a win, and below the point where a notebook that
// could be split is left on one thread.
func BenchmarkScanSplitThreshold(b *testing.B) {
	ctx := context.Background()
	db, nb := benchDB(b, 1200)
	db.shards = 8

	query := make([]float32, benchDim)
	rng := rand.New(rand.NewPCG(3, 4))
	for d := range query {
		query[d] = float32(rng.NormFloat64())
	}

	for _, per := range []int{1200, 600, 300, 150, 100} {
		db.rowsPerShard = per
		b.Run(fmt.Sprintf("rowsPerShard=%04d", per), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				hits, err := db.SearchSegments(ctx, nb, query, "bench+tp1", benchTop)
				if err != nil {
					b.Fatal(err)
				}
				if len(hits) == 0 {
					b.Fatal("no hits")
				}
			}
		})
	}
}

// BenchmarkScanSmallCorpus is the case a real notebook is in for years: a few hundred
// chunks, where the only new thing the sharded scan does is ask how big the corpus
// is. If that question were expensive it would be a regression on every search.
func BenchmarkScanSmallCorpus(b *testing.B) {
	ctx := context.Background()
	db, nb := benchDB(b, 600)

	query := make([]float32, benchDim)
	for d := range query {
		query[d] = 1
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		hits, err := db.SearchSegments(ctx, nb, query, "bench+tp1", benchTop)
		if err != nil {
			b.Fatal(err)
		}
		if len(hits) == 0 {
			b.Fatal("no hits")
		}
	}
}
