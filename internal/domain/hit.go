package domain

// Hit is one segment recalled by a search, with the score that recalled it and the
// name of the source it belongs to.
//
// It lives in the leaf package because it is the value that crosses the boundary
// between storage and retrieval: the store knows how to find rows and what the
// source was called, and retrieval must not go and read either of those again to
// render a result.
//
// Score carries no unit: it is whatever the recall scan measured, kept for logging
// and debugging. Ordering is that scan's and nothing rewrites it afterwards, so two
// Scores side by side are not a comparison any rule defines.
type Hit struct {
	Segment    Segment `json:"segment"`
	Score      float64 `json:"score"`
	SourceName string  `json:"source_name"`
}
