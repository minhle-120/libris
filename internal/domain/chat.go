package domain

// A chat is one conversation inside a notebook, and a message is one turn of it.
// They are separate from the research machinery on purpose: this is what a user
// reads and comes back to, which outlives the model that wrote it, the chunks it
// cited and the settings that were saved when it was asked.

// Chat is one conversation with a notebook.
type Chat struct {
	ID         string `json:"id"`
	NotebookID string `json:"notebook_id"`
	// Title is the first question asked, truncated. Editing it is not built yet,
	// so it is write-once and the UI does not offer a pen.
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"` // unix seconds
}

// Role is who said a message. Only these two reach a user; the research model's
// tool calls are a trace, not a message, and are never listed here.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// MessageStatus is how an assistant turn ended, in the words a reader needs.
//
// It is not the research loop's status: "synthesize" is control flow — evidence in
// hand, answer not yet written — and no reader cares. What a reader cares about is
// whether the sentence in front of them is grounded, and these five say that.
type MessageStatus string

const (
	// MessageAnswered means grounded prose, with citations that resolve.
	MessageAnswered MessageStatus = "answered"
	// MessageNoInfo means the library does not contain the answer. Its content is
	// the reason, and it is excluded from later turns' history: an earlier "I
	// couldn't find that" is not knowledge, and left in the transcript it reads as
	// evidence that the fact is absent from the world.
	MessageNoInfo MessageStatus = "no-info"
	// MessageUngrounded means the model answered in prose instead of searching.
	// The prose is what it said, verbatim, and the UI marks it as unchecked rather
	// than dressing it up as a cited answer.
	MessageUngrounded MessageStatus = "ungrounded"
	// MessageCanceled means the user stopped the turn. Nothing was written.
	MessageCanceled MessageStatus = "canceled"
	// MessageInterrupted means the answer was under way and did not finish: the
	// model server died, or the process did. The partial text is kept, because
	// losing a half-written answer is worse than showing it with a label.
	MessageInterrupted MessageStatus = "interrupted"
)

// Message is one turn of a chat.
type Message struct {
	ID        int64         `json:"id"`
	ChatID    string        `json:"chat_id"`
	Role      Role          `json:"role"`
	Content   string        `json:"content"`
	Status    MessageStatus `json:"status,omitempty"`
	CreatedAt int64         `json:"created_at"` // unix seconds

	// Citations is what the [[Chunk N]] labels in Content resolved to, frozen at
	// the time the answer was written. Empty for a user message.
	Citations []Citation `json:"citations,omitempty"`
}

// MessageUpdate is what a finished turn writes back into the row that was appended
// before its answer streamed. It lives in domain rather than in the store because
// the caller that finishes a turn (app) and the thing that writes the row (the
// store) must not have to agree by accident.
//
// Trace is the research transcript, already encoded: nothing that reads messages
// back is able to parse it, which is what keeps the reasoning out of a model's
// prompt and out of the API.
type MessageUpdate struct {
	Content   string
	Status    MessageStatus
	Citations []Citation
	Trace     string
}

// Citation is a resolved [[Chunk N]].
//
// It is self-contained, and that is the load-bearing property. A re-ingest deletes
// a source's segment rows and writes new ones (PutSegments), so a citation storing
// only a segment id would dangle the first time a source was rebuilt — and every
// answer in the chat history would lose its sources. So the quote, the provenance
// and the asset are copied out of the segment at write time, and history still
// renders, still cites and still opens, whatever has since happened to the corpus.
//
// The provenance is what the citation route is built from:
// #/source/<id>?page=7&c=1200-1640 for text, ?page=7 for a raster.
type Citation struct {
	Chunk   int    `json:"chunk"` // the number in [[Chunk N]], per turn, 1-based
	Segment string `json:"segment_id"`
	DocID   string `json:"doc_id"`

	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`

	Kind    Kind `json:"kind"`
	Ordinal int  `json:"ordinal"`

	// Quote is the chunk's text as it was retrieved. For a page raster with no text
	// of its own it is empty, and the asset is what opens.
	Quote string `json:"quote,omitempty"`

	// Asset is the file a citation opens: the page raster. Nil for a text chunk in a
	// paginated document, where the original plus the span is enough. Asset.Path is
	// never sent; the client fetches /api/assets/<id>.
	Asset *Asset `json:"asset,omitempty"`

	Span Provenance `json:"span"`
}

// CitationFrom freezes a chunk into something a citation can be built from later,
// after the registry, the retrieval and the model have all been forgotten.
func CitationFrom(chunk int, sourceName string, seg Segment) Citation {
	c := Citation{
		Chunk:      chunk,
		Segment:    seg.ID,
		DocID:      seg.DocID,
		SourceID:   seg.SourceID,
		SourceName: sourceName,
		Kind:       seg.Kind,
		Ordinal:    seg.Ordinal,
		Quote:      seg.Text,
		Span:       seg.Span,
	}
	if seg.Asset != nil {
		a := *seg.Asset
		// The path is dropped as well as the pointer shared. `json:"-"` already keeps
		// it off the wire, and a citation is a record of what was cited rather than a
		// handle for opening files off disk — anything that needs the bytes looks the
		// asset up by id, which is also how the browser gets them.
		a.Path = ""
		c.Asset = &a
	}
	return c
}
