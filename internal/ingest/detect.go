package ingest

import (
	"net/http"
	"path/filepath"
	"strings"

	"libris/internal/domain"
)

// Detect classifies a file. Extension wins because it is more specific than
// sniffing (a .md and a .json are both text/plain to http.DetectContentType);
// the sniffed MIME type is the fallback for extensionless input.
func Detect(name string, head []byte) domain.SourceKind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return domain.SourcePDF
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff":
		return domain.SourceImage
	case ".mp3", ".wav", ".flac", ".m4a", ".ogg", ".opus":
		return domain.SourceAudio
	case ".txt", ".md", ".markdown", ".rst", ".tex", ".csv", ".tsv",
		".json", ".yaml", ".yml", ".toml", ".ini", ".xml", ".html", ".htm",
		".log", ".go", ".py", ".js", ".ts", ".c", ".h", ".cpp", ".hpp",
		".rs", ".java", ".kt", ".sh", ".sql", ".css":
		return domain.SourceText
	}

	switch mt := http.DetectContentType(head); {
	case strings.HasPrefix(mt, "application/pdf"):
		return domain.SourcePDF
	case strings.HasPrefix(mt, "image/"):
		return domain.SourceImage
	case strings.HasPrefix(mt, "audio/"):
		return domain.SourceAudio
	case strings.HasPrefix(mt, "text/"), mt == "application/json":
		return domain.SourceText
	}
	return domain.SourceUnsupported
}
