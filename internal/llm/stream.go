package llm

import (
	"bufio"
	"bytes"
	"io"
)

// sse reads server-sent events, ignoring comments and non-data fields. MCP and
// chat streaming share this format, so it stays transport-generic.
type sse struct {
	sc *bufio.Scanner
}

const maxSSELine = 4 << 20

func newSSE(r io.Reader) *sse {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return &sse{sc: sc}
}

// Next returns the next data payload. ok is false at end of stream.
func (s *sse) Next() (payload []byte, ok bool, err error) {
	for s.sc.Scan() {
		line := bytes.TrimSpace(s.sc.Bytes())
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		rest, found := bytes.CutPrefix(line, []byte("data:"))
		if !found {
			continue // event:, id:, retry:
		}
		return bytes.TrimSpace(rest), true, nil
	}
	if err := s.sc.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}
