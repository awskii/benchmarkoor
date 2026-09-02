package blocklog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// erigonPayload is the record Erigon emits, taken verbatim from a validated
// block on a node built with --debug.slow-block-threshold=0.
const erigonPayload = `{"level":"warn","msg":"Slow block","block":{"number":1,"hash":"0x5c6519e89d3b01dc9846d2b67a07202efd45fcd35d380beada32f7be406fd22d","gas_used":12000,"tx_count":1},"timing":{"execution_ms":0.829174,"state_read_ms":0.001553,"state_hash_ms":0.142427,"commit_ms":0.042781,"total_ms":1.015935},"throughput":{"mgas_per_sec":11.81},"state_reads":{"accounts":3,"storage_slots":0,"code":0},"state_writes":{"accounts":1,"storage_slots":0,"code":0},"cache":{"account":{"hits":3,"misses":0,"hit_rate":1},"storage":{"hits":0,"misses":0,"hit_rate":0},"code":{"hits":0,"misses":0,"hit_rate":0}}}`

func TestErigonParser_ParseLine(t *testing.T) {
	parser := NewErigonParser()

	tests := []struct {
		name      string
		line      string
		wantOK    bool
		checkJSON func(t *testing.T, data map[string]any)
	}{
		{
			// Erigon's terminal format leaves a trailing space after the message.
			name:   "non-TTY line with all fields",
			line:   `[WARN] [09-01|22:20:12.372] ` + erigonPayload + ` `,
			wantOK: true,
			checkJSON: func(t *testing.T, data map[string]any) {
				t.Helper()

				assert.Equal(t, "warn", data["level"])
				assert.Equal(t, "Slow block", data["msg"])

				block := data["block"].(map[string]any)
				assert.Equal(t, float64(1), block["number"])
				assert.Equal(t, "0x5c6519e89d3b01dc9846d2b67a07202efd45fcd35d380beada32f7be406fd22d", block["hash"])
				assert.Equal(t, float64(12000), block["gas_used"])
				assert.Equal(t, float64(1), block["tx_count"])

				timing := data["timing"].(map[string]any)
				assert.Equal(t, 0.829174, timing["execution_ms"])
				assert.Equal(t, 0.001553, timing["state_read_ms"])
				assert.Equal(t, 0.142427, timing["state_hash_ms"])
				assert.Equal(t, 0.042781, timing["commit_ms"])
				assert.Equal(t, 1.015935, timing["total_ms"])

				throughput := data["throughput"].(map[string]any)
				assert.Equal(t, 11.81, throughput["mgas_per_sec"])

				stateReads := data["state_reads"].(map[string]any)
				assert.Equal(t, float64(3), stateReads["accounts"])
				assert.Equal(t, float64(0), stateReads["storage_slots"])
				assert.Equal(t, float64(0), stateReads["code"])

				stateWrites := data["state_writes"].(map[string]any)
				assert.Equal(t, float64(1), stateWrites["accounts"])

				account := data["cache"].(map[string]any)["account"].(map[string]any)
				assert.Equal(t, float64(3), account["hits"])
				assert.Equal(t, float64(0), account["misses"])
				assert.Equal(t, float64(1), account["hit_rate"])
			},
		},
		{
			// On a TTY the level is wrapped in colour codes and the space
			// between it and the timestamp disappears.
			name:   "TTY line with ANSI escape codes",
			line:   "\x1b[33mWARN\x1b[0m[09-01|22:20:12.372] " + erigonPayload + " ",
			wantOK: true,
			checkJSON: func(t *testing.T, data map[string]any) {
				t.Helper()

				assert.Equal(t, "Slow block", data["msg"])
				assert.Equal(t, float64(1), data["block"].(map[string]any)["number"])
				assert.Equal(t, 0.142427, data["timing"].(map[string]any)["state_hash_ms"])
			},
		},
		{
			name:   "DBUG level envelope",
			line:   `[DBUG] [09-01|22:20:12.372] ` + erigonPayload,
			wantOK: true,
			checkJSON: func(t *testing.T, data map[string]any) {
				t.Helper()

				assert.Equal(t, "Slow block", data["msg"])
			},
		},
		{
			name:   "EROR level envelope",
			line:   `[EROR] [09-01|22:20:12.372] ` + erigonPayload,
			wantOK: true,
			checkJSON: func(t *testing.T, data map[string]any) {
				t.Helper()

				assert.Equal(t, "Slow block", data["msg"])
			},
		},
		{
			name:   "padded level",
			line:   `[WARN ] [09-01|22:20:12.372] ` + erigonPayload,
			wantOK: true,
			checkJSON: func(t *testing.T, data map[string]any) {
				t.Helper()

				assert.Equal(t, "Slow block", data["msg"])
			},
		},
		{
			name:   "right message but no timing object",
			line:   `[WARN] [09-01|22:20:12.372] {"level":"warn","msg":"Slow block","block":{"hash":"0xabc"}}`,
			wantOK: false,
		},
		{
			// Erigon logs plenty of other JSON at WARN; only the slow block
			// message carries this schema.
			name:   "JSON payload from another message",
			line:   `[WARN] [09-01|22:20:12.372] {"level":"warn","msg":"Something else","block":{"number":1}}`,
			wantOK: false,
		},
		{
			name:   "ordinary erigon log line",
			line:   `[INFO] [09-01|22:20:12.372] [1/6 OtterSync] Downloading                 progress="98.5% 12/13"`,
			wantOK: false,
		},
		{
			name:   "invalid JSON",
			line:   `[WARN] [09-01|22:20:12.372] {not valid json}`,
			wantOK: false,
		},
		{
			name:   "missing timestamp",
			line:   `[WARN] ` + erigonPayload,
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := parser.ParseLine(tt.line)

			assert.Equal(t, tt.wantOK, ok)

			if tt.wantOK {
				require.NotNil(t, result)

				var parsed map[string]any
				err := json.Unmarshal(result, &parsed)
				require.NoError(t, err)

				tt.checkJSON(t, parsed)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestErigonParser_ClientType(t *testing.T) {
	parser := NewErigonParser()
	assert.Equal(t, "erigon", string(parser.ClientType()))
}
