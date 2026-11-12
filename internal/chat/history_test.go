package chat

import (
	"strings"
	"testing"

	"libris/internal/llm"
)

// History is not one thing. The research agent gets flattened text and the
// synthesiser gets a conversation, and merging them is the tempting mistake: a
// research model that sees old tool-shaped turns answers the shape instead of the
// question.

func convo() []HistoryMessage {
	return []HistoryMessage{
		{Role: llm.RoleUser, Text: "Who measured the erosion?"},
		{Role: llm.RoleAssistant, Text: "Smith measured it in 1990 [[Chunk 2]]."},
		{Role: llm.RoleUser, Text: "And what did he find?"},
		{Role: llm.RoleAssistant, Text: "3mm a year [[Chunk 1]][[Chunk 3]]."},
	}
}

func TestResearchHistoryIsFlat(t *testing.T) {
	got := ResearchHistory(convo(), 8)

	if strings.Contains(got, "[[Chunk") {
		t.Errorf("citations survived into the research prompt: %q", got)
	}
	if !strings.Contains(got, "Query: Who measured the erosion?") {
		t.Errorf("no flattened query: %q", got)
	}
	// The label is a framing cue: prior answers were evidence-derived, so refine
	// them rather than re-deriving from nothing.
	if !strings.Contains(got, "Synthesizer answer: Smith measured it in 1990.") {
		t.Errorf("answers are not framed as synthesizer output: %q", got)
	}
	if strings.Contains(got, "Assistant:") {
		t.Errorf("the framing cue was softened: %q", got)
	}
	// Oldest first, so the conversation reads the way it happened.
	if strings.Index(got, "Who measured") > strings.Index(got, "what did he find") {
		t.Errorf("out of order: %q", got)
	}
}

// A turn the library could not answer is not a fact about the subject. Both halves
// go: leaving the query in place would keep the dead end while removing only its
// outcome, which is how a model learns to repeat a search that already failed.
func TestNoInfoTurnsAreRememberedByNobody(t *testing.T) {
	msgs := []HistoryMessage{
		{Role: llm.RoleUser, Text: "What about the 2031 projections?", Exclude: true},
		{Role: llm.RoleAssistant, Text: "I searched and found nothing on 2031.", Exclude: true},
		{Role: llm.RoleUser, Text: "How fast is the coast retreating?"},
	}

	if got := ResearchHistory(msgs, 8); strings.Contains(strings.ToLower(got), "2031") {
		t.Errorf("the failed turn is in the research history: %q", got)
	}
	for _, m := range SynthesisHistory(msgs, 8) {
		if strings.Contains(textOf(m), "2031") {
			t.Errorf("the failed turn is in the synthesis history: %q", textOf(m))
		}
	}
	if len(SynthesisHistory(msgs, 8)) != 1 {
		t.Errorf("%d messages survived, want the one live query", len(SynthesisHistory(msgs, 8)))
	}
}

// The window is the most recent turns, and a window of zero means the user turned
// conversational memory off — honoured literally rather than rounded up to one.
func TestHistoryWindow(t *testing.T) {
	msgs := convo()

	if got := ResearchHistory(msgs, 2); !strings.Contains(got, "what did he find") || strings.Contains(got, "Who measured") {
		t.Errorf("window of 2 gave %q", got)
	}
	if got := SynthesisHistory(msgs, 1); len(got) != 1 || textOf(got[0]) == "" {
		t.Errorf("window of 1 gave %+v", got)
	}
	if got := ResearchHistory(msgs, 0); got != "" {
		t.Errorf("a window of 0 gave %q, want no memory", got)
	}
	if got := SynthesisHistory(msgs, 0); got != nil {
		t.Errorf("a window of 0 gave %d messages", len(got))
	}
	if got := ResearchHistory(msgs, 99); !strings.Contains(got, "Who measured") {
		t.Errorf("a window wider than the conversation truncated it: %q", got)
	}
}

// The synthesiser is the model a user reads, so it needs real turns: pronouns resolve
// because "he" appeared in a genuine user message.
func TestSynthesisHistoryIsAConversation(t *testing.T) {
	got := SynthesisHistory(convo(), 8)
	if len(got) != 4 {
		t.Fatalf("%d messages, want the whole exchange", len(got))
	}
	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser, llm.RoleAssistant}
	for i, m := range got {
		if m.Role != wantRoles[i] {
			t.Errorf("message %d is %q, want %q", i, m.Role, wantRoles[i])
		}
		if strings.Contains(textOf(m), "[[Chunk") {
			t.Errorf("message %d still carries a citation label: %q", i, textOf(m))
		}
	}
	if !strings.Contains(textOf(got[1]), "Smith measured it in 1990.") {
		t.Errorf("answer text was damaged: %q", textOf(got[1]))
	}
}

// An assistant turn that was nothing but citations, or was never written, is not
// something to remember.
func TestEmptyAndForeignTurnsAreDropped(t *testing.T) {
	msgs := []HistoryMessage{
		{Role: llm.RoleUser, Text: "q1"},
		{Role: llm.RoleAssistant, Text: "   "},
		{Role: llm.RoleAssistant, Text: "[[Chunk 1]]"},
		{Role: llm.RoleSystem, Text: "a system note"},
		{Role: llm.RoleTool, Text: "a tool result"},
		{Role: llm.RoleUser, Text: "q2"},
	}

	got := SynthesisHistory(msgs, 8)
	if len(got) != 2 {
		t.Fatalf("%d messages, want the two user turns: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Role != llm.RoleUser {
			t.Errorf("survived: %q", m.Role)
		}
	}
	if got := ResearchHistory(msgs, 8); got != "Query: q1\n\nQuery: q2" {
		t.Errorf("research history = %q", got)
	}
}

// A blank turn is not a turn.
func TestNoHistoryAtAll(t *testing.T) {
	if got := ResearchHistory(nil, 8); got != "" {
		t.Errorf("%q", got)
	}
	if got := SynthesisHistory(nil, 8); got != nil {
		t.Errorf("%+v", got)
	}
}

func TestStripCitations(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Measured [[Chunk 1]] in 1990.", "Measured in 1990."},
		{"3mm a year[[Chunk 1]][[Chunk 2]] since then.", "3mm a year since then."},
		{"[[Chunk 12]]", ""},
		{"No tags here.", "No tags here."},
		{"First line [[Chunk 1]].\n\nSecond paragraph.", "First line.\n\nSecond paragraph."},
		{"trailing tag [[Chunk 3]]", "trailing tag"},
	}
	for _, c := range cases {
		if got := StripCitations(c.in); got != c.want {
			t.Errorf("StripCitations(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Without "Current query:" a model reading a flattened history researches the last
// thing in the block, which after a follow-up is not the question being asked.
func TestSeedContext(t *testing.T) {
	if got := seedContext("", "What about 2031?"); got != "What about 2031?" {
		t.Errorf("no history: %q", got)
	}
	if got := seedContext("   \n ", "q"); got != "q" {
		t.Errorf("blank history: %q", got)
	}
	got := seedContext("Query: earlier\n\nSynthesizer answer: 3mm.", "and what did he find?")
	if !strings.HasSuffix(got, "\n\nCurrent query: and what did he find?") {
		t.Errorf("%q", got)
	}
	if !strings.HasPrefix(got, "Query: earlier") {
		t.Errorf("history was dropped: %q", got)
	}
}
