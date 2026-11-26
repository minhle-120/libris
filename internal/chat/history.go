package chat

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"libris/internal/llm"
)

// HistoryMessage is one persisted turn, in the shape the store will hand it over.
//
// Exclude is the turn's own verdict on whether it should be remembered: a question
// the library could not answer is not a fact about the subject, and feeding it back
// invites the model to treat "I couldn't find X" as evidence that X is absent from
// the world. The user's message is excluded with its answer, not just the answer —
// leaving the query in place would leave the dead end in the conversation while
// removing only its outcome.
type HistoryMessage struct {
	Role    llm.Role
	Text    string
	Exclude bool
}

// citation is the grammar the synthesiser is instructed to emit. It is a key into the
// evidence the turn was built from, so the same pattern serves rendering, citation
// lookup and history sanitising.
//
// The inside is a run of labels rather than the prompt's instructed shape, because the
// shape a model writes is not the shape it was told to use: `[[Chunk 1], [Chunk 2]]`, and
// even `[[Chunk 1], [[Chunk 2]]`, both arrive in real answers. Read only the instructed
// shape and a grouped answer leaves its labels in the history, where the next turn finds
// them and copies them. The run cannot match prose — `[[Some note]]` has no `Chunk` in it
// — so nothing that is not a citation is deleted.
var citation = regexp.MustCompile(`\[\[(?:Chunk \d+[\s\[\],]*)+\]\]`)

// A label and the space it stood in go together, in the direction the sentence needs:
// "1990 [[Chunk 2]]." must come back as "1990." rather than "1990 .", because the
// full stop belongs to the sentence and not to the citation, while "a [[Chunk 1]] b"
// must not come back as "ab". Paragraph breaks are left exactly where they were —
// this function trims, it does not reflow.
func StripCitations(text string) string {
	if !strings.Contains(text, "[[Chunk") {
		return text
	}

	var out []byte
	prev := 0
	for _, loc := range citation.FindAllStringIndex(text, -1) {
		out = append(out, text[prev:loc[0]]...)

		next := loc[1]
		for next < len(text) && (text[next] == ' ' || text[next] == '\t') {
			next++
		}
		rest := text[next:]
		switch {
		case isCloser(firstRune(rest)):
			// The sentence's own punctuation follows, so the space the citation was
			// standing in goes too.
			for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t') {
				out = out[:len(out)-1]
			}
		case isWord(lastRune(string(out))) && isWord(firstRune(rest)):
			// Two words the citation was keeping apart.
			out = append(out, ' ')
		}
		prev = next
	}
	out = append(out, text[prev:]...)
	return strings.TrimSpace(string(out))
}

// isCloser reports whether a rune ends a sentence rather than starting one, which is
// what decides whether a space in front of a removed citation survives.
func isCloser(r rune) bool {
	return r == 0 || strings.ContainsRune(`.,;:!?)]}"'%`, r)
}

func isWord(r rune) bool {
	return r == '-' || r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func firstRune(s string) rune {
	if s == "" {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func lastRune(s string) rune {
	if s == "" {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// window returns the most recent n turns, oldest first. An empty window means the
// user turned conversational memory off, which is honoured literally rather than
// rounded up to one: a turn with no memory is a legitimate thing to want.
func window(msgs []HistoryMessage, n int) []HistoryMessage {
	if n <= 0 {
		return nil
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	return msgs
}

// ResearchHistory flattens past turns into one block of text.
//
// Flattened rather than as alternating role messages, for two reasons. It is
// cheaper, and it keeps tool_choice="required" from being influenced by prior
// tool-shaped turns — a model that sees old assistant messages carrying tool calls
// gets it into its head that this turn's first message may too.
//
// Past answers are labelled "Synthesizer answer:", not "Assistant:", which is a
// framing cue rather than a formatting one: it tells the research model that what
// came before was derived from evidence, so it refines that instead of re-deriving
// from nothing.
func ResearchHistory(msgs []HistoryMessage, n int) string {
	var b strings.Builder
	for _, m := range window(msgs, n) {
		if m.Exclude {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		if m.Role == llm.RoleAssistant {
			// Stripped before the emptiness test, so an answer that was nothing but
			// citations does not become a labelled blank line.
			text = StripCitations(text)
			if text == "" {
				continue
			}
		}
		switch m.Role {
		case llm.RoleUser:
			b.WriteString("\n\nQuery: ")
			b.WriteString(text)
		case llm.RoleAssistant:
			b.WriteString("\n\nSynthesizer answer: ")
			b.WriteString(text)
		}
	}
	return strings.TrimPrefix(b.String(), "\n\n")
}

// SynthesisHistory renders past turns as real role messages.
//
// The synthesiser is the model a user reads, so it needs an actual conversation:
// pronouns resolve because "he" appeared in a genuine user turn, and the answer
// continues in the register the chat is already in. The research agent gets text for
// the same reason in reverse — it should not mistake remembered tool calls for
// pending ones.
func SynthesisHistory(msgs []HistoryMessage, n int) []llm.Message {
	var out []llm.Message
	for _, m := range window(msgs, n) {
		if m.Exclude {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		if m.Role == llm.RoleAssistant {
			text = StripCitations(text)
			if text == "" { // an answer that was nothing but citations
				continue
			}
		}
		if m.Role != llm.RoleUser && m.Role != llm.RoleAssistant {
			continue
		}
		out = append(out, llm.Message{Role: m.Role, Content: text})
	}
	return out
}

// seedContext is how the research turn is opened: history and the current question
// in one user message, with the question last and labelled.
//
// The label matters more than it looks: without "Current query:" a model reading a
// flattened history answers the last thing in the block, which after a follow-up is
// indistinguishable from the question it is meant to research.
func seedContext(history, question string) string {
	if strings.TrimSpace(history) == "" {
		return question
	}
	return history + "\n\nCurrent query: " + question
}
