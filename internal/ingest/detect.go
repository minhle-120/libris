package ingest

import (
	"net/http"
	"path/filepath"
	"strings"

	"libris/internal/domain"
)

// Detect classifies a file.
//
// Magic bytes decide for PDF, image, recording and moving picture: a PNG saved as notes.txt would
// otherwise be read as text and produce chunks of mojibake, and the same is true of a
// WAV saved as notes.md. Extension decides for everything else, because it is more
// specific than sniffing — a .md and a .json are both text/plain to
// http.DetectContentType — and for text there is no wrong-but-harmless reading to
// avoid.
//
// So a recording or a video is decided by its bytes first, and that guard is not decoration:
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
	// .webm stays here rather than joining the video list below, and it is the one case
	// where the bytes are useless: an EBML header answers "video/webm" for a screen
	// recording and for an Opus recording of a meeting alike, because WebM names a
	// container and not what is in it. The extension is all the evidence there is, and
	// the audio work took that evidence first — a recording of a meeting is worth more
	// rows than a rare recording of a picture.
	case ".mp3", ".wav", ".flac", ".m4a", ".ogg", ".opus", ".aac", ".webm", ".aiff", ".aif":
		return domain.SourceAudio
	// There is no .ts here, though TypeScript and MPEG transport stream share the letters
	// and http.DetectContentType knows the second. A text source is worth more rows than
	// a rare one, so the extension list is where that collision is settled.
	case ".mp4", ".m4v", ".mov", ".mkv", ".avi", ".wmv", ".flv", ".mpg", ".mpeg", ".ogv", ".3gp":
		return domain.SourceVideo
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
	if kind, ok := sniffContainer(head); ok {
		return kind
	}
	return domain.SourceUnsupported
}

// audioBrands are the ISO base-media brands that mean audio-only.
//
// Three letters, which is what makes "M4A " and "M4A " the same answer and "mp42" not
// collide with anything here. Anything else is out: mp42, isom and avc1 carry pictures,
// and the rule below is that a brand that means pictures is video.
var audioBrands = map[string]bool{"M4A": true, "M4B": true, "M4P": true}

// videoBrands are the brands that name pictures.
//
// Most of them are redundant beside a file named .mp4, and they are listed anyway for
// the case the extension cannot serve: a video whose name was lost, or renamed .txt
// twice removed from the phone that shot it. Three letters is enough because these
// profiles number their revisions in the fourth — iso8, mp41, mp42, 3gp5.
var videoBrands = map[string]bool{
	"iso": true, // the base profile, and what most encoders write whatever they contain
	"mp4": true, "mp7": true,
	"avc": true, "av0": true, // H.264 and AV1
	"M4V": true, // Apple's video-only
	"qt ": true, // QuickTime, whose brand is padded rather than numbered
	"3gp": true, "das": true,
}

// sniffContainer recognises the media containers net/http cannot. Its sniffing table
// covers WAV, ID3-tagged MP3 and Ogg — and no video at all: a brand header answers
// "application/octet-stream" there, which is the answer that lets the extension win,
// and a container that says nothing while the extension says ".md" is precisely the
// mojibake case Detect exists to prevent.
//
// Four bytes are enough for FLAC and ADIF, which is why the longer cases check their own
// length rather than the whole head: Detect is handed 512 bytes in the pipeline, and the
// sniff must not need them to be that long to be right.
//
// What it refuses matters as much. The box that opens an M4A opens an MP4 too, so the
// brand after "ftyp" has to name one modality or the other: a brand that means audio is
// audio, a brand that means pictures is video, and a brand that means nothing this build
// knows about comes back unknown so the extension gets its say.
func sniffContainer(head []byte) (domain.SourceKind, bool) {
	if len(head) < 4 {
		return domain.SourceUnsupported, false
	}
	switch {
	case string(head[0:4]) == "fLaC":
		return domain.SourceAudio, true
	case len(head) < 12:
		return domain.SourceUnsupported, false
	case string(head[4:8]) == "ftyp": // ISO base media: M4A, MP4, …
		brand := string(head[8:11])
		switch {
		case audioBrands[brand]:
			return domain.SourceAudio, true
		case videoBrands[brand]:
			return domain.SourceVideo, true
		}
		return domain.SourceUnsupported, false
	case string(head[0:4]) == "FORM" && (string(head[8:12]) == "AIFF" || string(head[8:12]) == "AIFC"):
		return domain.SourceAudio, true
	case string(head[0:4]) == "ADIF":
		return domain.SourceAudio, true
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0 && head[1]&0x01 == 1: // MPEG audio frame sync
		return domain.SourceAudio, true
	}
	return domain.SourceUnsupported, false
}
