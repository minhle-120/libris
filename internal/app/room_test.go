package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/ingest"
)

// The other half of the memory arrangement idle_test.go describes: a job asks the answering model
// to leave the card before it opens the file. What is protected here is the order — the ask is
// worth nothing if the file is already being decoded by the time it is made — and the fact that it
// is an ask. A machine that cannot free the memory still reads the file.
func TestIngestAsksForRoomBeforeItReadsTheFile(t *testing.T) {
	rec := &recorder{}
	var asks atomic.Int64
	svc, db, _ := wiredService(t,
		&orderedProcessor{fake: &fakeProcessor{}, rec: rec},
		func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil },
		nil,
		func(context.Context) error {
			asks.Add(1)
			rec.add("room")
			return nil
		})

	nb, err := svc.CreateNotebook(context.Background(), "Quantum")
	if err != nil {
		t.Fatal(err)
	}
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("%PDF-1.4\nscan"), "paper.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the source to become ready", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})

	// One ask, and it came first. An ask after the decode would be a request for memory the job
	// has already allocated without.
	if got := rec.snapshot(); len(got) != 2 || got[0] != "room" || got[1] != "read the file" {
		t.Errorf("events = %v, want room then read the file", rec.snapshot())
	}
	if got := asks.Load(); got != 1 {
		t.Errorf("%d asks for one job, want the one", got)
	}
}

// The ask is a courtesy. The alternative to running without the memory is refusing the upload over
// a memory arrangement, and the second is worse: the visible failure would be a file that will not
// go in, blaming a model server that happens to be busy.
func TestARoomAskThatFailsStillReadsTheFile(t *testing.T) {
	var asks atomic.Int64
	svc, db, _ := wiredService(t, &fakeProcessor{},
		func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil },
		nil,
		func(context.Context) error {
			asks.Add(1)
			return errors.New("the router is not listening")
		})

	nb, err := svc.CreateNotebook(context.Background(), "Quantum")
	if err != nil {
		t.Fatal(err)
	}
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("%PDF-1.4\nscan"), "paper.pdf")
	if err != nil {
		t.Fatal(err)
	}

	waitUntil(t, "the source to become ready despite the failed ask", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})
	if got := asks.Load(); got != 1 {
		t.Errorf("%d asks, want 1", got)
	}
}

// recorder is the order things happened in, as the worker caused them. Two hooks write to it, so
// a test can name which came first rather than that both happened.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// orderedProcessor is fakeProcessor with the one thing the fake does not record: that it ran.
type orderedProcessor struct {
	fake *fakeProcessor
	rec  *recorder
}

func (p *orderedProcessor) Process(ctx context.Context, path string) (*ingest.Result, error) {
	p.rec.add("read the file")
	return p.fake.Process(ctx, path)
}
