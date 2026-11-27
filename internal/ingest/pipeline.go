package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/tools"
)

// Doc is a derived document. One uploaded file can produce several: a PDF
// yields its text and its page rasters, which are indexed and cited as if they
// were separate documents.
type Doc struct {
	ID    string
	Title string
	Kind  domain.Kind
}

// Result is everything the embed stage needs, and nothing that requires a
// model.
type Result struct {
	Source   domain.Source
	Docs     []Doc
	Segments []domain.Segment
	// Findings is what the run noticed on the way to the status below: a stage that gave up,
	// a program this machine does not have. A file that produced some segments and lost others
	// is Ready with findings; a file that produced none is Unsupported, and the findings say
	// whose fault that was — the file's, or the machine's.
	Findings    []domain.Finding
	Unsupported string // non-empty reason when nothing could be extracted
}

// Processor turns files into segments. It never returns an error for a file it
// cannot understand: that is recorded in Result so a batch keeps going.
type Processor struct {
	cfg   config.Config
	sink  Sink
	pdf   *PDF
	audio *Audio
	video *Video
}

// New validates the config and takes the programs it will run, already resolved. They arrive
// resolved because resolution is a question about folders and operating systems rather than
// about this file, and because a Processor is built per job: the answer is asked for when an
// upload happens rather than remembered from whenever the process started.
func New(cfg config.Config, sink Sink, progs tools.PDF, audio tools.Audio, video tools.Video) (*Processor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, errors.New("ingest: sink is required")
	}
	return &Processor{
		cfg:  cfg,
		sink: sink,
		pdf: &PDF{
			Text:   progs.Text,
			Raster: progs.Raster,
			DPI:    cfg.PDFDPI,
		},
		audio: &Audio{
			FFmpeg:  audio.FFmpeg,
			Seconds: cfg.AudioClipSeconds,
			Overlap: cfg.AudioClipOverlap,
		},
		video: &Video{
			FFmpeg:  video.FFmpeg,
			FFprobe: video.FFprobe,
			Seconds: cfg.VideoClipSeconds,
			Overlap: cfg.VideoClipOverlap,
		},
	}, nil
}

// Process reads one file at path. The caller is responsible for keeping the
// original; this only writes derived assets through the sink.
func (p *Processor) Process(ctx context.Context, path string) (*Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("ingest: %s is a directory", path)
	}
	if info.Size() > p.cfg.MaxFileBytes() {
		return nil, fmt.Errorf("ingest: %s is %d bytes, over the %d byte limit",
			filepath.Base(path), info.Size(), p.cfg.MaxFileBytes())
	}

	head, err := readHead(path, 512)
	if err != nil {
		return nil, err
	}
	sum, err := hashFile(path)
	if err != nil {
		return nil, err
	}

	name := filepath.Base(path)
	res := &Result{
		Source: domain.Source{
			ID:     sum,
			Name:   name,
			Kind:   Detect(name, head),
			SHA256: sum,
			Bytes:  info.Size(),
		},
	}

	switch res.Source.Kind {
	case domain.SourceText:
		p.text(res, path)
	case domain.SourcePDF:
		p.pdfDoc(ctx, res, path)
	case domain.SourceImage:
		p.image(ctx, res, path)
	case domain.SourceAudio:
		p.audioDoc(ctx, res, path)
	case domain.SourceVideo:
		p.videoDoc(ctx, res, path)
	default:
		res.Unsupported = "unsupported file type"
	}
	return res, nil
}

func (p *Processor) text(res *Result, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		// The Go error stops here on purpose: the row's sentence is the one below, and what
		// an OS error adds is a path inside the data directory. There is no finding either —
		// this is the whole outcome for this file, so saying it twice would only make the
		// row longer, not clearer.
		res.Unsupported = "unreadable file"
		return
	}

	text := textFrom(res.Source.Name, data)
	if strings.TrimSpace(text) == "" {
		res.Unsupported = "no text content after extraction"
		return
	}

	doc := Doc{ID: res.Source.ID, Title: res.Source.Name, Kind: domain.KindText}
	res.Docs = append(res.Docs, doc)
	res.Segments = textSegments(doc, res.Source.ID, text, p.cfg)
}

func (p *Processor) image(ctx context.Context, res *Result, path string) {
	if _, err := inspectImage(path); err != nil {
		// Same reason as the text branch: the format is the finding, and it is already the
		// status.
		res.Unsupported = "image format has no Go decoder"
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	asset, err := p.sink.PutFile(ctx, res.Source.ID+"/original"+ext, path)
	if err != nil {
		// The one place a storage error's own words are kept: a full disk and a rejected
		// filename want different answers from whoever is reading the row.
		res.Unsupported = fmt.Sprintf("could not store image: %v", err)
		return
	}

	doc := Doc{ID: res.Source.ID, Title: res.Source.Name, Kind: domain.KindImage}
	res.Docs = append(res.Docs, doc)
	res.Segments = append(res.Segments, domain.Segment{
		ID:       segmentID(doc.ID, 0),
		SourceID: res.Source.ID,
		DocID:    doc.ID,
		Kind:     domain.KindImage,
		Ordinal:  0,
		Asset:    &asset,
	})
}

func (p *Processor) pdfDoc(ctx context.Context, res *Result, path string) {
	textDocID := res.Source.ID
	pageDocID := res.Source.ID + "#pages"

	// Derived document 1: page text.
	pages, err := p.pdf.Pages(ctx, path)
	switch {
	case errors.Is(err, ErrNoPDFText):
		res.Findings = append(res.Findings, domain.ToolMissing(p.pdf.Text.Name))
	case err != nil:
		res.Findings = append(res.Findings, domain.StageFailed("page text", err))
	default:
		doc := Doc{ID: textDocID, Title: res.Source.Name, Kind: domain.KindText}
		res.Docs = append(res.Docs, doc)
		res.Segments = append(res.Segments, pagedTextSegments(doc, res.Source.ID, pages, p.cfg)...)
	}

	// Derived document 2: page rasters, one segment per page.
	//
	// The text pass runs first, so its page count is already here, and pdftotext
	// separates pages with a form feed whether or not a page has any text in it — the
	// count is the document's, not the text's. Zero when that pass failed, which the
	// rasteriser reads as "render this in one process".
	assets, err := p.pdf.PageImages(ctx, path, p.sink, res.Source.ID+"/pages", len(pages))
	switch {
	case errors.Is(err, ErrNoPDFRaster):
		res.Findings = append(res.Findings, domain.ToolMissing(p.pdf.Raster.Name))
	case err != nil:
		res.Findings = append(res.Findings, domain.StageFailed("page images", err))
	default:
		doc := Doc{ID: pageDocID, Title: res.Source.Name + " [pages]", Kind: domain.KindImage}
		res.Docs = append(res.Docs, doc)
		for i, asset := range assets {
			a := asset
			res.Segments = append(res.Segments, domain.Segment{
				ID:       segmentID(doc.ID, i),
				SourceID: res.Source.ID,
				DocID:    doc.ID,
				Kind:     domain.KindImage,
				Ordinal:  i,
				Asset:    &a,
				Span:     domain.Provenance{Page: i + 1},
			})
		}
	}

	if len(res.Segments) == 0 && len(res.Findings) > 0 {
		// Nothing at all came out, so this is a status rather than a note — but which sentence
		// depends on whose fault it was. Every finding naming a missing program means no PDF
		// would have been read on this machine, and blaming the file would send someone looking
		// for a broken PDF on a box that simply lacks poppler. The findings below the row name
		// which programs; this sentence only says who is at fault.
		if onlyToolMissing(res.Findings) {
			res.Unsupported = "no PDF tools on this machine"
			return
		}
		res.Unsupported = "PDF produced no segments"
	}
}

// onlyToolMissing is whether every finding blames the machine rather than the file.
func onlyToolMissing(findings []domain.Finding) bool {
	for _, f := range findings {
		if f.Code != domain.FindingToolMissing {
			return false
		}
	}
	return len(findings) > 0
}

// pagedTextSegments chunks the document's text as a whole, so a chunk may run
// across a page break, then resolves each chunk back onto the page — or the two
// pages — it covers. Chunking each page in isolation would instead cut every
// paragraph that straddles a boundary, which is exactly where a 50-word
// overlap exists to help.
func pagedTextSegments(doc Doc, sourceID string, pages []string, cfg config.Config) []domain.Segment {
	joined, runs := JoinPages(pages)
	pieces := SplitText(joined, cfg.ChunkWords, cfg.ChunkOverlap)

	out := make([]domain.Segment, 0, len(pieces))
	for i, piece := range pieces {
		out = append(out, domain.Segment{
			ID:       segmentID(doc.ID, i),
			SourceID: sourceID,
			DocID:    doc.ID,
			Kind:     domain.KindText,
			Ordinal:  i,
			Text:     piece.Text,
			Span:     MapSpan(runs, piece.Start, piece.End),
		})
	}
	return out
}

// textSegments handles a source with no pages: offsets are into the file
// itself, so Provenance carries no page at all.
func textSegments(doc Doc, sourceID, text string, cfg config.Config) []domain.Segment {
	pieces := SplitText(text, cfg.ChunkWords, cfg.ChunkOverlap)
	out := make([]domain.Segment, 0, len(pieces))
	for i, piece := range pieces {
		out = append(out, domain.Segment{
			ID:       segmentID(doc.ID, i),
			SourceID: sourceID,
			DocID:    doc.ID,
			Kind:     domain.KindText,
			Ordinal:  i,
			Text:     piece.Text,
			Span:     domain.Provenance{CharStart: piece.Start, CharEnd: piece.End},
		})
	}
	return out
}

// audioDoc turns a recording into one segment per clip: the clip's bytes as the asset, no
// text, and the window it covers in the span.
//
// A clip is not a citation a reader can quote, and ADR 0010 says so rather than hiding it:
// the words arrive when the answering model hears the clip, not before.
func (p *Processor) audioDoc(ctx context.Context, res *Result, path string) {
	clips, err := p.audio.Clips(ctx, path, p.sink, res.Source.ID+"/clips")
	switch {
	case errors.Is(err, ErrNoAudioTools):
		// No recording anywhere could be read on this machine, so this is not the file's
		// fault — the same distinction the PDF branch makes, with the same finding naming
		// the program to go and get.
		res.Findings = append(res.Findings, domain.ToolMissing(p.audio.FFmpeg.Name))
		res.Unsupported = "no ffmpeg on this machine"
	case errors.Is(err, ErrAudioTooLong):
		res.Findings = append(res.Findings, domain.StageFailed("clips", err))
		res.Unsupported = "recording is longer than libris slices"
	case err != nil:
		res.Findings = append(res.Findings, domain.StageFailed("clips", err))
		res.Unsupported = "recording could not be sliced"
	case len(clips) == 0:
		// The recording decoded, and every window of it was below the audible floor. The
		// file is fine; there is simply nothing in it to answer from.
		res.Unsupported = "recording holds nothing audible"
	default:
		doc := Doc{ID: res.Source.ID, Title: res.Source.Name, Kind: domain.KindAudio}
		res.Docs = append(res.Docs, doc)
		for i, clip := range clips {
			a := clip.Asset
			res.Segments = append(res.Segments, domain.Segment{
				ID:       segmentID(doc.ID, i),
				SourceID: res.Source.ID,
				DocID:    doc.ID,
				Kind:     domain.KindAudio,
				Ordinal:  i,
				Asset:    &a,
				Span:     domain.Provenance{T0: clip.T0, T1: clip.T1},
			})
		}
	}
}

// videoDoc turns one video into two documents: the pictures of it, as clips of frames, and
// its soundtrack, as clips of sound. ADR 0012 — the model that is shown the pictures is not
// told what is being said over them, so a video that produced only frames would be answering
// from half of what it was given.
//
// Two documents rather than one, and two ordinal spaces, because that is what a PDF already
// is: page text and page rasters, each with its own clip four. A row of one document and a
// row of the other can cover the same second, and the spans are what say so — nothing here
// fuses them into a row, because nothing can put a second file in a segment that holds one
// asset, and nothing has measured whether one fused vector beats two in the same space.
//
// Either half can be absent, and neither absence is the file's fault. A screen recording with
// no microphone, and a meeting's audio carried in a container whose brand claims pictures,
// are both files libris should read rather than complain about; what it says about them is
// that the document is not there, which is not a finding — a finding names something the
// person could go and fix.
func (p *Processor) videoDoc(ctx context.Context, res *Result, path string) {
	streams, err := p.video.Probe(ctx, path)
	switch {
	case errors.Is(err, ErrNoVideoTools):
		// Not this file's fault, and the sentence has to name two programs where a
		// recording names one.
		res.Findings = append(res.Findings,
			domain.ToolMissing(p.video.FFmpeg.Name), domain.ToolMissing(p.video.FFprobe.Name))
		res.Unsupported = "no ffmpeg and ffprobe on this machine"
		return
	case err != nil:
		res.Findings = append(res.Findings, domain.StageFailed("video", err))
		res.Unsupported = "video could not be read"
		return
	}

	var (
		frames  []clipRow
		sound   []clipRow
		failure string // the sentence for the half that tried and could not
	)

	if streams.Video {
		clips, err := p.video.Frames(ctx, path, streams, p.sink, res.Source.ID+"/clips")
		switch {
		case err != nil:
			res.Findings = append(res.Findings, domain.StageFailed("frames", err))
			failure = "the pictures could not be sliced"
		default:
			for _, c := range clips {
				frames = append(frames, clipRow{asset: c.Asset, t0: c.T0, t1: c.T1})
			}
		}
	}

	if streams.Audio {
		clips, err := p.audio.Clips(ctx, path, p.sink, res.Source.ID+"/sound")
		switch {
		case err != nil:
			res.Findings = append(res.Findings, domain.StageFailed("soundtrack", err))
			failure = "the soundtrack could not be sliced"
		default:
			for _, c := range clips {
				sound = append(sound, clipRow{asset: c.Asset, t0: c.T0, t1: c.T1})
			}
		}
	}

	if len(frames) > 0 {
		clipDoc(res, res.Source.ID, res.Source.Name, domain.KindVideo, frames)
	}
	if len(sound) > 0 {
		// The suffix is how a PDF's rasters are named beside their text, and it earns its
		// keep the same way: the person reading the document list knows which half of the
		// file these rows are, before they open one.
		clipDoc(res, res.Source.ID+"#sound", res.Source.Name+" [sound]", domain.KindAudio, sound)
	}

	if len(frames) == 0 && len(sound) == 0 {
		// Whatever the container claimed, there was nothing in it to answer from — or the
		// half that could have been said so itself, in the finding above.
		res.Unsupported = failure
		if res.Unsupported == "" {
			res.Unsupported = "video holds nothing libris can read"
		}
	}
}

// clipRow is one clip of either kind: a file and the window it covers. Both slicers answer
// with a type of this shape, and the document builder below does not care which one handed it
// over.
type clipRow struct {
	asset  domain.Asset
	t0, t1 float64
}

// clipDoc appends one document and its rows. A document per clip kind is what keeps
// segmentID from putting two rows at the same id when a video's clip four and its
// soundtrack's clip four are both real.
func clipDoc(res *Result, id, title string, kind domain.Kind, clips []clipRow) {
	res.Docs = append(res.Docs, Doc{ID: id, Title: title, Kind: kind})
	for i, c := range clips {
		asset := c.asset
		res.Segments = append(res.Segments, domain.Segment{
			ID:       segmentID(id, i),
			SourceID: res.Source.ID,
			DocID:    id,
			Kind:     kind,
			Ordinal:  i,
			Asset:    &asset,
			Span:     domain.Provenance{T0: c.t0, T1: c.t1},
		})
	}
}

func segmentID(docID string, ordinal int) string {
	return fmt.Sprintf("%s:%04d", docID, ordinal)
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:read], nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
