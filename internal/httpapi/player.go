package httpapi

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	txtmpl "text/template"

	"libris/internal/domain"
)

// The player page exists because a browser cannot be trusted with a bare audio file.
//
// Navigating to an audio original asks the browser to decide, on its own, whether it
// plays or downloads. The decision turns on the MIME type it was handed and on whether
// it decodes that container at all, and both answers vary by machine — which is how
// "open the original" ends as a downloaded file nobody asked for. So libris answers the
// navigation with a page of its own, and the browser keeps the one job it does
// reliably: render an <audio> element.
//
// The page is its own file rather than the SPA bundle because it is a document a tab
// shows by itself: it is never part of the app's state, and the citation that opens it
// must work before a bundle has loaded.
//
//go:embed player.html
var playerPage string

// Rendered with text/template on purpose, and the one dynamic value escaped by hand.
//
// html/template would be the reflex, and it is wrong here for a reason worth writing
// down: it re-tokenises the script body, and the comments in it — the ones naming the
// #t=start,end protocol and why the play edge is held — come out of the served page as
// blank lines. A static file with its comments intact, holding one name that is escaped
// on the way in, is both the readable and the honest form. The other two fields are
// numbers and a path built from a stored id.
var playerTemplate = txtmpl.Must(txtmpl.New("player").Parse(playerPage))

// handlePlayer serves that page for a recording: the destination of an audio citation
// and of the source row's own button, so "open the original" means the same thing on
// every machine — it opens, and it plays.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.GetSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "get source", err)
		return
	}
	// Only something with a timeline has anything to play here: a recording, or a video and
	// the soundtrack cut from it. Any other kind arriving at this URL is a link bug, and
	// saying so is more use than a page of dead controls.
	if source.Kind != domain.SourceAudio && source.Kind != domain.SourceVideo {
		writeError(w, http.StatusBadRequest, "that source has nothing to play")
		return
	}
	// The row can outlive the file. Check it here rather than let the element's own
	// error handler report it, because that handler cannot tell a missing file from a
	// codec the browser has never heard of — and only one of those two is worth a 404.
	if _, err := os.Stat(source.OriginalPath); err != nil {
		s.log.Warn("stored file missing", "path", source.OriginalPath, "err", err)
		writeError(w, http.StatusNotFound, "the stored file is missing from disk")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := playerTemplate.Execute(w, playerView(source)); err != nil {
		s.log.Warn("player page", "err", err)
	}
}

// playerView is the page's whole data. The clip window is deliberately absent: it
// arrives in the hash, which the browser never sends to the server.
func playerView(source domain.Source) map[string]any {
	clips := "clips"
	if source.SegmentCount == 1 {
		clips = "clip"
	}
	return map[string]any{
		// The name came from whoever uploaded the file, so it is the only field here
		// with anything to escape. Escaped with html/template's own escaper rather than
		// a local one, so the rule matches the rest of the app.
		"Name":   template.HTMLEscapeString(source.Name),
		"Src":    "/api/sources/" + source.ID + "/original",
		"Detail": fmt.Sprintf("%d %s · %d kB", source.SegmentCount, clips, source.Bytes/(1<<10)),
		// Which element the page puts the file in. The script does not care: it holds
		// one element by id, and a window of seconds means the same thing to both.
		"Video": source.Kind == domain.SourceVideo,
	}
}
