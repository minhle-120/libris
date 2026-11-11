package index

import (
	"strings"
)

// EmbeddingGemma 2 is trained with short task-instruction prefixes on its text
// inputs. The model card is explicit that the prefix is not decoration:
//
//	"Using the right prefix improves quality; omitting it still works but reduces
//	precision. Prefixes apply to text only. Pass images, video, and audio without
//	any prefix."
//
// Retrieval is an asymmetric task, so the two sides take different prefixes. What
// we store is always the document side, and the document prefix is identical for
// the SearchQuery and QuestionAnswering presets — they differ only in how the
// query is steered. That is what lets one embedding serve both question styles,
// and it only holds as long as no query-side prefix reaches a stored vector.
const (
	// QuerySearch steers a free-form search.
	QuerySearch = "task: search result | query: "
	// QueryAnswer steers a question expected to be answered from a passage.
	QueryAnswer = "task: question answering | query: "
)

// PromptScheme names this prefix scheme and is recorded with every vector. The
// store's model column is what identifies a vector space (ADR 0004), and the
// prefix is part of that space: a corpus embedded with prefixes and one embedded
// without them both read as "Embeddinggemma-2, 768-d" and cannot be compared.
// Bump the number whenever the wording below changes, and re-embed.
const PromptScheme = "tp1"

// maxTitleRunes bounds the document title. A filename is free quality — the model
// is told what the passage came from — but it is also unbounded user input, and
// every token here is taken from the one-ubatch budget the embedding server
// enforces. 120 runes is a long name and roughly 30 tokens.
const maxTitleRunes = 120

// titleNone is the card's own word for a document with no title.
const titleNone = "none"

// ModelIdentity is what gets written into the store beside a vector: the model the
// server was told to use, plus the prompt scheme that shaped it.
func ModelIdentity(model string) string {
	if strings.TrimSpace(model) == "" {
		return PromptScheme
	}
	return model + "+" + PromptScheme
}

// DocumentPrompt wraps one stored passage in the prefix the model was trained to
// expect. It returns the prompt only; the segment's own text is never rewritten,
// because citations quote it.
func DocumentPrompt(title, text string) string {
	return "title: " + cleanTitle(title) + " | text: " + text
}

// cleanTitle makes a source name safe to place inside the prompt's own structure.
// A pipe would open a second field, a newline would end the prefix line, and an
// arbitrarily long name would eat the input's token budget — so pipes become
// slashes, whitespace collapses, and the length is capped on a rune boundary.
func cleanTitle(title string) string {
	title = strings.ReplaceAll(title, "|", "/")
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return titleNone
	}
	if r := []rune(title); len(r) > maxTitleRunes {
		title = string(r[:maxTitleRunes])
	}
	return title
}

// SteerQuery applies a query-side prefix. Nothing calls it yet — retrieval is not
// built — but the strings live here so the query and document halves of the pair
// cannot drift apart into two different vector spaces.
func SteerQuery(prefix, query string) string {
	return prefix + strings.TrimSpace(query)
}
