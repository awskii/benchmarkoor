package blocklog

import (
	"encoding/json"
	"regexp"

	"github.com/ethpandaops/benchmarkoor/pkg/client"
)

// erigonLogPattern matches Erigon slow block log lines (after ANSI stripping).
// Format: [{LEVEL}] [{timestamp}] {JSON payload}
// Example: [WARN] [09-01|22:20:12.372] {"level":"warn","msg":"Slow block",...}
//
// Erigon abbreviates two of its level names, DBUG and EROR. On a TTY the level
// is colorised and the brackets and following space are dropped, so the
// stripped line reads WARN[09-01|...] instead; the separators are matched
// loosely so a change in level padding cannot silence the parser.
var erigonLogPattern = regexp.MustCompile(
	`^\[?(?:TRACE|DBUG|INFO|WARN|EROR|CRIT)\s*\]?\s*\[[^\]]+\]\s+(\{.+\})\s*$`,
)

// erigonParser parses JSON payloads from Erigon client slow block logs.
type erigonParser struct{}

// NewErigonParser creates a new Erigon log parser.
func NewErigonParser() Parser {
	return &erigonParser{}
}

// Ensure interface compliance.
var _ Parser = (*erigonParser)(nil)

// ParseLine extracts JSON from an Erigon slow block log line.
func (p *erigonParser) ParseLine(line string) (json.RawMessage, bool) {
	// Strip ANSI escape codes — Erigon colorises the level when on a TTY.
	line = ansiPattern.ReplaceAllString(line, "")

	matches := erigonLogPattern.FindStringSubmatch(line)
	if len(matches) < 2 {
		return nil, false
	}

	jsonStr := matches[1]

	// Erigon has no dedicated slow block logger, so the message is the only
	// thing separating this payload from any other JSON logged at WARN. The
	// timing object is required too, so a bare message cannot be stored as a
	// record that carries none of the metrics.
	var probe struct {
		Msg    string           `json:"msg"`
		Timing *json.RawMessage `json:"timing"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &probe); err != nil {
		return nil, false
	}

	if probe.Msg != "Slow block" || probe.Timing == nil {
		return nil, false
	}

	return json.RawMessage(jsonStr), true
}

// ClientType returns the client type.
func (p *erigonParser) ClientType() client.ClientType {
	return client.ClientErigon
}
