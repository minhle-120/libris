package ingest

import (
	"net/http"
	"path/filepath"
	"strings"

	"libris/internal/domain"
)

// Detect classifies a file.
//
// Magic bytes decide for PDF and image: a PNG saved as notes.txt would
// otherwise be read as text and produce chunks of mojibake. Extension decides
// for everything else, because it is more specific than sniffing — a .md and a
// .json are both text/plain to http.DetectContentType — and for text there is no
// wrong-but-harmless reading to avoid.
func Detect(name string, head []byte) domain.SourceKind {
	if kind := sniffBinary(head); kind != domain.SourceUnsupported {
		return kind
	}
	// A recording is refused by its bytes, before the extension gets a say. The
	// extension-wins rule below is for text, and applying it to a WAV named
	// notes.md would index a hundred megabytes of samples as mojibake chunks.
	// (Recordings are not an input modality at all: ADR 0003.)
	if mt := http.DetectContentType(head); strings.HasPrefix(mt, "audio/") {
		return domain.SourceUnsupported
	}

	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return domain.SourcePDF
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff":
		return domain.SourceImage
	case ".txt", ".md", ".markdown", ".rst", ".tex", ".csv", ".tsv",
		".json", ".yaml", ".yml", ".toml", ".ini", ".xml", ".html", ".htm",
		".log", ".go", ".py", ".js", ".ts", ".c", ".h", ".cpp", ".hpp",
		".rs", ".java", ".kt", ".sh", ".sql", ".css":
		return domain.SourceText
	}

	// Last resort: an extensionless source file, recognised as text.
	if mt := http.DetectContentType(head); strings.HasPrefix(mt, "text/") || mt == "application/json" {
		return domain.SourceText
	}
	return domain.SourceUnsupported
}

// sniffBinary reports the modality only where the bytes are decisive. Anything
// it is unsure about comes back unsupported so the extension gets a say.
func sniffBinary(head []byte) domain.SourceKind {
	switch mt := http.DetectContentType(head); {
	case strings.HasPrefix(mt, "application/pdf"):
		return domain.SourcePDF
	case strings.HasPrefix(mt, "image/"):
		return domain.SourceImage
	}
	// Audio is not classified at all, so an .mp3 lands in unsupported and says so, on
	// the way out rather than half-indexed: recordings were dropped as an input
	// modality because a slice had no words to cite. See ADR 0003.
	return domain.SourceUnsupported
}
