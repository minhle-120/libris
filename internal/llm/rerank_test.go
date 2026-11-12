package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"libris/internal/llm"
)

// Text documents go out as a bare array of strings, which is what every rerank
// server reads. Media is the exception, so it must not leak into the common case:
// a {"text": …} object where a string was expected is a 400 from some servers.
func TestRerankDocumentsMarshalAsStrings(t *testing.T) {
	srv, rec := server(t, 200, `{"results":[{"index":0,"relevance_score":1}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

	if _, err := c.Rerank(context.Background(), llm.RerankRequest{
		Query:     "q",
		Documents: textDocs("first", "second"),
	}); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatal(err)
	}
	docs, _ := sent["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("documents = %v", sent["documents"])
	}
	for i, want := range []string{"first", "second"} {
		s, ok := docs[i].(string)
		if !ok || s != want {
			t.Errorf("documents[%d] = %T %v, want the string %q", i, docs[i], docs[i], want)
		}
	}
}

// A raster is scored as the image itself. A document with an image marshals in the
// object form llama.cpp parses for a multimodal reranker, and one with both says
// so — a page image plus the text on it is one document, not two.
func TestRerankDocumentWithImage(t *testing.T) {
	srv, rec := server(t, 200, `{"results":[{"index":0,"relevance_score":1}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

	_, err := c.Rerank(context.Background(), llm.RerankRequest{
		Query: "the chart",
		Documents: []llm.RerankDoc{
			llm.ImageDoc("data:image/png;base64,AAA"),
			{Text: "caption", Image: "data:image/png;base64,BBB"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var sent struct {
		Documents []any `json:"documents"`
	}
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Documents) != 2 {
		t.Fatalf("documents = %v", sent.Documents)
	}

	imageOnly, _ := sent.Documents[0].(map[string]any)
	if imageOnly["image"] != "data:image/png;base64,AAA" {
		t.Errorf("image document = %v", sent.Documents[0])
	}
	if _, hasText := imageOnly["text"]; hasText {
		t.Errorf("image-only document sent a text key: %v", imageOnly)
	}

	both, _ := sent.Documents[1].(map[string]any)
	if both["image"] != "data:image/png;base64,BBB" || both["text"] != "caption" {
		t.Errorf("text+image document = %v", both)
	}
}

// The response is in the server's order, not ours, and it may omit documents. A
// caller that read scores positionally would attach them to the wrong chunks —
// citations pointing at passages that were merely nearby.
func TestRerankScoresAlignToTheDocumentsSent(t *testing.T) {
	srv, rec := server(t, 200, `{"results":[
		{"index":2,"relevance_score":0.9},
		{"index":0,"relevance_score":0.4}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

	scores, err := c.RerankScores(context.Background(), "rerank-model", "q", textDocs("a", "b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 3 {
		t.Fatalf("%d scores for 3 documents", len(scores))
	}
	if scores[2] != 0.9 || scores[0] != 0.4 {
		t.Errorf("scores = %v, want doc 2 first and doc 1 (unscored) last", scores)
	}
	if scores[1] != 0 {
		t.Errorf("the omitted document scored %v, want the zero that ranks it last", scores[1])
	}

	// The name the user typed reaches the server. Sent without it, a reranker
	// setting is decoration.
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "rerank-model" {
		t.Errorf("request model = %q, want the configured reranker name", sent.Model)
	}
}

// Out-of-range and over-long answers mean the server did not score the set we sent.
// Silently truncating would drop evidence with no trace.
func TestRerankScoresRejectsImpossibleAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"index out of range": `{"results":[{"index":7,"relevance_score":0.9}]}`,
		"more results than documents": `{"results":[
			{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.8},
			{"index":2,"relevance_score":0.7}]}`,
	} {
		srv, _ := server(t, 200, body, nil)
		c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

		if _, err := c.RerankScores(context.Background(), "m", "q", textDocs("a", "b")); err == nil {
			t.Errorf("%s: accepted without error", name)
		}
	}
}

func TestRerankScoresRefusesNoDocuments(t *testing.T) {
	srv, _ := server(t, 200, `{"results":[]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

	_, err := c.RerankScores(context.Background(), "m", "q", nil)
	if err == nil || !strings.Contains(err.Error(), "no documents") {
		t.Errorf("err = %v, want the no-documents guard", err)
	}
}
