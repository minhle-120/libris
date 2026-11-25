package llm_test

import (
	"context"
	"strings"
	"testing"

	"libris/internal/llm"
)

// The shape server-context.cpp writes today. It is pinned here rather than invented,
// because the whole value of the call is that the engine answers for itself: a decode
// that quietly defaults to false on a renamed key would withhold every page in the
// notebook with nothing in the log to say why.
const propsBody = `{
  "model_alias": "libris-chat",
  "modalities": { "vision": true, "video": false, "audio": false },
  "media_marker": "<__media__>",
  "total_slots": 1
}`

// Both shapes of chat URL have to find it: a base ending in /v1 is the same server one
// level up, and /props is not an OpenAI endpoint, so it lives at the root.
func TestPropsReadsWhatTheEngineSaysItCanBeShown(t *testing.T) {
	for _, suffix := range []string{"", "/v1"} {
		srv, rec := server(t, 200, propsBody, nil)
		c := newClient(t, llm.Config{ChatURL: srv.URL + suffix})

		caps, err := c.Props(context.Background())
		if err != nil {
			t.Fatalf("Props(%q): %v", suffix, err)
		}
		if rec.path != "/props" {
			t.Errorf("chat URL with suffix %q was asked at %q, want /props", suffix, rec.path)
		}
		if caps.Model != "libris-chat" || !caps.Vision {
			t.Errorf("caps = %+v", caps)
		}
		if caps.Audio || caps.Video {
			t.Errorf("a capability the server denied was read as true: %+v", caps)
		}
	}
}

// A server that does not answer /props — one libris does not manage — is not a failure
// of the question. The caller decides what "it did not say" means; Props only declines
// to invent an answer.
func TestPropsReportsAServerThatWillNotSay(t *testing.T) {
	srv, _ := server(t, 404, `{"error":{"message":"not found"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	if caps, err := c.Props(context.Background()); err == nil {
		t.Errorf("a refusal was read as caps: %+v", caps)
	}
}

func TestCapsAllowsIsTheModelsAnswerNotAguess(t *testing.T) {
	var unknown *llm.Caps
	if !unknown.Allows(llm.MediaImage) {
		t.Error("nobody asking was read as a no: pages would be withheld from a model that can see them")
	}

	blind := &llm.Caps{Model: "text-only"}
	if blind.Allows(llm.MediaImage) {
		t.Error("a model with no vision tower was allowed an image")
	}

	// A kind with no wire shape is refused here as well as by MediaPart, so the two
	// halves of the seam cannot disagree about whether a modality exists.
	seeAll := &llm.Caps{Vision: true, Audio: true, Video: true}
	if seeAll.Allows(llm.MediaKind("hologram")) {
		t.Error("a modality this build cannot send was allowed anyway")
	}
}

// The refusal is the only thing a reader of a failed turn gets, so it has to name the
// model to go and change, the modality that is missing, and how much it cost.
func TestMediaUnsupportedNamesWhatToChange(t *testing.T) {
	one := &llm.MediaUnsupported{Kind: llm.MediaImage, Model: "libris-chat", Count: 1}
	if !strings.Contains(one.Error(), "libris-chat") ||
		!strings.Contains(one.Error(), "vision") ||
		!strings.Contains(one.Error(), "1 image") ||
		strings.Contains(one.Error(), "images") {
		t.Errorf("one: %q", one.Error())
	}

	many := &llm.MediaUnsupported{Kind: llm.MediaImage, Count: 3}
	if !strings.Contains(many.Error(), "3 images") || !strings.Contains(many.Error(), "the answering model") {
		t.Errorf("many: %q", many.Error())
	}
}
