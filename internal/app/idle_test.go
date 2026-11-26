package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"libris/internal/config"
	"libris/internal/domain"
)

// The rules around the idle release, without waiting on a real ingest to make them true.
//
// What is being protected here is a promise the settings page cannot show: an embedding model
// holds memory on the way to an answer, and after the uploads stop that memory belongs to the
// model that gives one. The rules are that the release waits for the queue, happens once per
// batch rather than once per file, and never happens to a machine that is still working.
func TestIdleReleaseWaitsForTheQueueAndFiresOnce(t *testing.T) {
	window := embedIdleWindow
	embedIdleWindow = 20 * time.Millisecond
	t.Cleanup(func() { embedIdleWindow = window })

	var fires atomic.Int64
	svc := &Service{
		jobs:    make(chan string, queueDepth),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		jobCtx:  context.Background(),
		release: func(context.Context) error { fires.Add(1); return nil },
	}

	// A job waiting is not an idle machine, however long the window is left to run. This is the
	// rule that keeps a twenty-file drop from putting the model to sleep between files.
	svc.jobs <- "still coming"
	svc.armIdleRelease()
	time.Sleep(5 * embedIdleWindow)
	if got := fires.Load(); got != 0 {
		t.Fatalf("%d releases while an upload was queued", got)
	}
	<-svc.jobs

	svc.armIdleRelease()
	waitUntil(t, "the idle release", func() bool { return fires.Load() == 1 })

	// Once, not on a loop. Nothing re-arms the clock until another job runs, so a quiet machine
	// asks once and then stops asking.
	time.Sleep(5 * embedIdleWindow)
	if got := fires.Load(); got != 1 {
		t.Errorf("%d releases after one quiet spell, want the one", got)
	}

	// Work arriving cancels a release that has not happened yet.
	svc.armIdleRelease()
	svc.cancelIdleRelease()
	time.Sleep(5 * embedIdleWindow)
	if got := fires.Load(); got != 1 {
		t.Errorf("%d releases; a cancelled release should not have happened", got)
	}
}

// Nobody to tell is a legitimate wiring — a server somebody else started — and the worker must
// not care.
func TestIdleReleaseDoesNothingWhenThereIsNobodyToTell(t *testing.T) {
	svc := &Service{
		jobs:   make(chan string, queueDepth),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		jobCtx: context.Background(),
	}
	// No window is waited out here: with nothing to tell, arming does not start a clock at all,
	// so there is nothing to wait for. The call is made directly to show that firing one would be
	// harmless too.
	svc.armIdleRelease()
	svc.releaseEmbedder(embedIdleWindow)
	svc.cancelIdleRelease()
}

// The whole path: a file goes in, its vectors get made, and when nothing else is waiting, the app
// says so to whoever runs the model server. The wait is the test's to shorten, not the machine's.
func TestAnUploadThatEndsHandsTheDeviceBack(t *testing.T) {
	window := embedIdleWindow
	embedIdleWindow = 30 * time.Millisecond
	t.Cleanup(func() { embedIdleWindow = window })

	var fires atomic.Int64
	var releaseCtx context.Context
	svc, db, _ := wiredService(t, &fakeProcessor{},
		func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil },
		func(ctx context.Context) error {
			releaseCtx = ctx
			fires.Add(1)
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

	// Nothing is handed back while the source is still being worked on — the release belongs to
	// the quiet after that, and not a moment sooner.
	waitUntil(t, "the idle release after one upload", func() bool { return fires.Load() == 1 })

	if got := sourceStatus(t, db, src.ID).Status; got != domain.StatusReady {
		t.Errorf("the source ended as %q; the release is meant to follow a finished ingest", got)
	}
	// The ingest context, not a background one: a release should not still be in flight to a
	// server that is being stopped.
	if releaseCtx != svc.jobCtx {
		t.Error("the release was not given the ingest context, so it could outlive shutdown")
	}

	time.Sleep(5 * embedIdleWindow)
	if got := fires.Load(); got != 1 {
		t.Errorf("%d releases for one upload, want the one", got)
	}
}
