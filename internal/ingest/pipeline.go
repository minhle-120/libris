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
	cfg  config.Config
	sink Sink
	pdf  *PDF
}

// New validates the config and takes the programs it will run, already resolved. They arrive
// resolved because resolution is a question about folders and operating systems rather than
// about this file, and because a Processor is built per job: the answer is asked for when an
// upload happens rather than remembered from whenever the process started.
func New(cfg config.Config, sink Sink, progs tools.PDF) (*Processor, error) {
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
	assets, err := p.pdf.PageImages(ctx, path, p.sink, res.Source.ID+"/pages")
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

// There is no audio path here. Recordings used to be sliced into timed clips and
// embedded whole; a clip had no text, so a hit was findable but the synthesiser
// could never quote it, and every answer leaning on one cited nothing. The
// modality went rather than stayed half-built. See ADR 0003.

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
