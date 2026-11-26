package ingest

import (
	"net/http"
	"path/filepath"
	"strings"

	"libris/internal/domain"
)

// Detect classifies a file.
//
// Magic bytes decide for PDF, image and recording: a PNG saved as notes.txt would
// otherwise be read as text and produce chunks of mojibake, and the same is true of a
// WAV saved as notes.md. Extension decides for everything else, because it is more
// specific than sniffing — a .md and a .json are both text/plain to
// http.DetectContentType — and for text there is no wrong-but-harmless reading to
// avoid.
//
// So a recording is decided by its bytes first, and that guard is not decoration:
// "the extension wins for text" is right for a .md and wrong for a WAV named notes.md,
// which would otherwise be indexed as a hundred megabytes of mojibake chunks. The
// sniffing has to be the thing that says "audio", rather than trusting what the
// extension says about the same file.
func Detect(name string, head []byte) domain.SourceKind {
	if kind := sniffBinary(head); kind != domain.SourceUnsupported {
		return kind
	}

	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return domain.SourcePDF
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff":
		return domain.SourceImage
	case ".mp3", ".wav", ".flac", ".m4a", ".ogg", ".opus", ".aac", ".webm", ".aiff", ".aif":
		return domain.SourceAudio
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
	case strings.HasPrefix(mt, "audio/"):
		return domain.SourceAudio
	}
	if isAudioContainer(head) {
		return domain.SourceAudio
	}
	return domain.SourceUnsupported
}

// isAudioContainer recognises the recording containers net/http cannot. Its sniffing
// table covers WAV, ID3-tagged MP3 and Ogg; a FLAC, an M4A, or an MP3 with no tag
// answers "application/octet-stream" there, which is the answer that lets the extension
// win — and a container that says nothing while the extension says ".md" is precisely
// the mojibake case Detect exists to prevent.
//
// Four bytes are enough for FLAC and ADIF, which is why the longer cases check their own
// length rather than the whole head: Detect is handed 512 bytes in the pipeline, and the
// sniff must not need them to be that long to be right.
//
// What it refuses matters as much. The box that opens an M4A opens an MP4 too, so the
// brand after "ftyp" has to name audio: video is not an input, and a file that looks
// like video belongs in unsupported rather than being sliced by ffmpeg and embedded as
// a clip of nothing.
func isAudioContainer(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch {
	case string(head[0:4]) == "fLaC":
		return true
	case len(head) < 12:
		return false
	case string(head[4:8]) == "ftyp": // ISO base media: M4A, MP4, …
		// Only the brands that mean audio-only. mp42, isom and avc1 carry video, and a
		// brand is not a promise about what is inside, so anything else is refused.
		switch string(head[8:11]) {
		case "M4A", "M4B", "M4P":
			return true
		}
		return false
	case string(head[0:4]) == "FORM" && (string(head[8:12]) == "AIFF" || string(head[8:12]) == "AIFC"):
		return true
	case string(head[0:4]) == "ADIF":
		return true
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0 && head[1]&0x01 == 1: // MPEG audio frame sync
		return true
	}
	return false
}
