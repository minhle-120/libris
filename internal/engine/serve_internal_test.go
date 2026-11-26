package engine

import "testing"

// The cap is one below the number of sections the preset declares, on purpose: the chat model
// and one embedder are the pair that answers a question, and the instance that loses a slot is
// the embedder the machine does not need resident between uploads.
//
// Not three, which is what it was when three sections were every model libris had. At that
// number the cap could only ever be reached, never exceeded, so nothing was ever evicted and a
// card held an idle embedder until the process died.
func TestServeDefaultsKeepOneChatAndOneEmbed(t *testing.T) {
	if got := (ServeOptions{}).withDefaults().ModelsMax; got != 2 {
		t.Errorf("ModelsMax defaults to %d, want 2", got)
	}
}
