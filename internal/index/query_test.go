package index

import (
	"context"
	"strings"
	"testing"

	"libris/internal/domain"
)

// A query is one input, in the query form, and nothing else. The asymmetry is the
// point: documents go out as "title: … | text: …" and queries as "task: … | query: …",
// and if either prefix ever reached the wrong side the two halves would stop living
// in the same space — while every score still computed.
func TestEmbedQuerySendsOneSteeredInput(t *testing.T) {
	c := newCapture(t)

	vec, err := c.embedder(t, 0).EmbedQuery(context.Background(), QuerySearch, "  coastal erosion  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != c.dim {
		t.Errorf("vector is %d long, want %d", len(vec), c.dim)
	}
	if c.calls != 1 {
		t.Errorf("%d requests, want 1", c.calls)
	}

	in := c.inputs(t, 0)
	if len(in) != 1 {
		t.Fatalf("sent %d inputs, want 1", len(in))
	}
	s, ok := in[0].(string)
	if !ok {
		t.Fatalf("input is %T, want a plain string", in[0])
	}
	if want := QuerySearch + "coastal erosion"; s != want {
		t.Errorf("sent %q, want %q", s, want)
	}
	// The document prefix would put the query in the space the documents live in,
	// which is exactly backwards: it is the documents that were written there.
	if strings.HasPrefix(s, "title:") {
		t.Errorf("a query was steered as a document: %q", s)
	}
}

func TestEmbedQueryRefusesNothingToEmbed(t *testing.T) {
	c := newCapture(t)
	for _, q := range []string{"", "   ", "\n\t"} {
		if _, err := c.embedder(t, 0).EmbedQuery(context.Background(), QuerySearch, q); err == nil {
			t.Errorf("an empty query %q was accepted", q)
		}
	}
	if c.calls != 0 {
		t.Errorf("%d requests for empty queries, want none", c.calls)
	}
}

// The dimension guard applies to queries as much as to chunks: a query vector of a
// different length cannot be compared with what is stored, and the search would say
// "no results" instead of "the server changed".
func TestEmbedQueryChecksDimensions(t *testing.T) {
	c := newCapture(t)
	if _, err := c.embedder(t, 64).EmbedQuery(context.Background(), QuerySearch, "anything"); err == nil {
		t.Fatal("a query vector of the promised dimension was accepted")
	}
}

func TestEmbedQueryNamesTheServer(t *testing.T) {
	c := newCapture(t)
	c.dim = 0 // an empty vector: the failure the message has to explain
	_, err := c.embedder(t, 0).EmbedQuery(context.Background(), QueryAnswer, "q")
	if err == nil {
		t.Fatal("an empty vector was accepted")
	}
	if !strings.Contains(err.Error(), c.srv.URL) {
		t.Errorf("error %q does not name the server the user has to fix", err)
	}
}

// The store's model column is written by this package and filtered on by retrieval.
// If the two ever computed it differently, a search would filter for a space nothing
// was written in and every source would quietly stop being findable.
func TestIdentityMatchesWhatEmbeddingsRecord(t *testing.T) {
	c := newCapture(t)
	e := c.embedder(t, 0)

	got, err := e.Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "x"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Model != e.Identity() {
		t.Errorf("stored model %q, but the search side would filter for %q", got[0].Model, e.Identity())
	}
	if want := "embeddinggemma-2+" + PromptScheme; e.Identity() != want {
		t.Errorf("identity = %q, want %q", e.Identity(), want)
	}
}
