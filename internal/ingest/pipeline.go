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
	Source      domain.Source
	Docs        []Doc
	Segments    []domain.Segment
	Warnings    []string
	Unsupported string // non-empty reason when nothing could be extracted
}

// Processor turns files into segments. It never returns an error for a file it
// cannot understand: that is recorded in Result so a batch keeps going.
type Processor struct {
	cfg  config.Config
	sink Sink
	pdf  *PDF
}

// New validates the config and probes for the external tools poppler supplies.
func New(cfg config.Config, sink Sink) (*Processor, error) {
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
			TextBin:   lookPath("pdftotext"),
			RasterBin: lookPath("pdftoppm"),
			DPI:       cfg.PDFDPI,
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
	if info.Size() > p.cfg.MaxFileBytes {
		return nil, fmt.Errorf("ingest: %s is %d bytes, over the %d byte limit",
			filepath.Base(path), info.Size(), p.cfg.MaxFileBytes)
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
		res.Unsupported = "audio needs transcription, which is not implemented yet"
	default:
		res.Unsupported = "unsupported file type"
	}
	return res, nil
}

func (p *Processor) text(res *Result, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		res.Warnings = append(res.Warnings, err.Error())
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
		res.Warnings = append(res.Warnings, err.Error())
		res.Unsupported = "image format has no Go decoder"
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	asset, err := p.sink.PutFile(ctx, res.Source.ID+"/original"+ext, path)
	if err != nil {
		res.Warnings = append(res.Warnings, err.Error())
		res.Unsupported = "could not store image"
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
		res.Warnings = append(res.Warnings, "pdftotext not found: PDF text extraction skipped")
	case err != nil:
		res.Warnings = append(res.Warnings, "pdf text: "+err.Error())
	default:
		doc := Doc{ID: textDocID, Title: res.Source.Name, Kind: domain.KindText}
		res.Docs = append(res.Docs, doc)
		ordinal := 0
		for i, page := range pages {
			if strings.TrimSpace(page) == "" {
				continue
			}
			for _, seg := range textSegments(doc, res.Source.ID, page, p.cfg) {
				seg.Ordinal = ordinal
				seg.ID = segmentID(doc.ID, ordinal)
				seg.Span.Page = i + 1
				ordinal++
				res.Segments = append(res.Segments, seg)
			}
		}
	}

	// Derived document 2: page rasters, one segment per page.
	assets, err := p.pdf.PageImages(ctx, path, p.sink, res.Source.ID+"/pages")
	switch {
	case errors.Is(err, ErrNoPDFRaster):
		res.Warnings = append(res.Warnings, "pdftoppm not found: PDF page images skipped")
	case err != nil:
		res.Warnings = append(res.Warnings, "pdf pages: "+err.Error())
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

	if len(res.Segments) == 0 && len(res.Warnings) > 0 {
		res.Unsupported = "PDF produced no segments"
	}
}

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
