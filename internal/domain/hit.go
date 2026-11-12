package domain

// Hit is one segment recalled by a search, with the score that recalled it and the
// name of the source it belongs to.
//
// It lives in the leaf package because it is the value that crosses the boundary
// between storage and retrieval: the store knows how to find rows and what the
// source was called, and retrieval must not go and read either of those again to
// render a result.
//
// Score carries no unit. Cosine and reranker relevance are different numbers on
// different scales, so it is recorded for logging and debugging only — ranking is
// decided by whichever stage wrote last, and only one stage's order is ever used.
type Hit struct {
	Segment    Segment `json:"segment"`
	Score      float64 `json:"score"`
	SourceName string  `json:"source_name"`
}
