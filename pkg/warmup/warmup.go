// Package warmup generates "warmup" engine_newPayload* requests by taking
// the new-payload calls from a test step, replacing the stateRoot with a
// fork-specific placeholder, and recomputing the blockHash so the EL
// client accepts the payload header and proceeds to execute it. The point
// of warmup is to populate caches before the real test runs.
package warmup

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Fork identifies an Ethereum hardfork. We currently only support Osaka.
type Fork string

const (
	// ForkOsaka is the only supported fork at the moment. Its header layout
	// includes withdrawalsRoot, blobGasUsed, excessBlobGas, parentBeaconRoot,
	// and requestsHash (EIP-7685).
	ForkOsaka Fork = "osaka"
)

// OsakaWarmupStateRoot is the placeholder stateRoot used in warmup payloads.
// It is intentionally non-empty so the EL deserializes a header successfully;
// the actual state-root mismatch is expected to be detected during execution,
// at which point the warmup has already populated caches.
const OsakaWarmupStateRoot = "0xe8d3a308a0d3fdaeed6c196f78aad4f9620b571da6dd5b886e7fa5eba07c83e0"

// IsValidFork returns true if the given fork identifier is supported.
func IsValidFork(fork string) bool {
	return Fork(fork) == ForkOsaka
}

// Generator transforms engine_newPayload* JSON-RPC lines into warmup
// equivalents (modified stateRoot + recomputed blockHash). Lines whose
// method is not engine_newPayload* are returned unchanged.
type Generator struct {
	fork      Fork
	stateRoot common.Hash
}

// NewGenerator returns a Generator for the given fork. Currently only
// ForkOsaka is accepted.
func NewGenerator(fork Fork) (*Generator, error) {
	if fork != ForkOsaka {
		return nil, fmt.Errorf("unsupported fork %q (only %q is supported)", fork, ForkOsaka)
	}

	return &Generator{
		fork:      fork,
		stateRoot: common.HexToHash(OsakaWarmupStateRoot),
	}, nil
}

// Transform rewrites a single JSON-RPC line. Non-engine_newPayload* lines
// pass through unchanged. For new-payload lines the stateRoot is replaced
// and the blockHash is recomputed before re-serializing the request.
func (g *Generator) Transform(line string) (string, error) {
	if strings.TrimSpace(line) == "" {
		return line, nil
	}

	var raw struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      json.RawMessage   `json:"id"`
		Method  string            `json:"method"`
		Params  []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return "", fmt.Errorf("parse jsonrpc line: %w", err)
	}

	if !strings.HasPrefix(raw.Method, "engine_newPayload") {
		return line, nil
	}

	if len(raw.Params) < 1 {
		return "", fmt.Errorf("%s: expected at least 1 param, got %d", raw.Method, len(raw.Params))
	}

	var data engine.ExecutableData
	if err := json.Unmarshal(raw.Params[0], &data); err != nil {
		return "", fmt.Errorf("parse executionPayload: %w", err)
	}

	// engine_newPayloadV3+ carry blobVersionedHashes (params[1]),
	// parentBeaconBlockRoot (params[2]) and (V4+) executionRequests (params[3]).
	versionedHashes, beaconRoot, requests, err := decodeExtraParams(raw.Method, raw.Params)
	if err != nil {
		return "", err
	}

	// Replace stateRoot and recompute blockHash. ExecutableDataToBlockNoHash
	// builds a block with all derived roots (txRoot, withdrawalsRoot,
	// requestsHash) without verifying the supplied blockHash matches.
	data.StateRoot = g.stateRoot

	block, err := engine.ExecutableDataToBlockNoHash(data, versionedHashes, beaconRoot, requests)
	if err != nil {
		return "", fmt.Errorf("build block from payload: %w", err)
	}

	data.BlockHash = block.Hash()

	newPayload, err := json.Marshal(&data)
	if err != nil {
		return "", fmt.Errorf("marshal warmup payload: %w", err)
	}

	raw.Params[0] = newPayload

	out, err := json.Marshal(&raw)
	if err != nil {
		return "", fmt.Errorf("marshal jsonrpc line: %w", err)
	}

	return string(out), nil
}

// TransformLines applies Transform to every line in the input slice. The
// returned slice has the same length and ordering as input.
func (g *Generator) TransformLines(lines []string) ([]string, error) {
	out := make([]string, len(lines))

	for i, line := range lines {
		transformed, err := g.Transform(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}

		out[i] = transformed
	}

	return out, nil
}

// decodeExtraParams pulls versionedHashes, parentBeaconBlockRoot, and
// executionRequests out of an engine_newPayload* params array. It tolerates
// older payload versions that omit later params.
func decodeExtraParams(
	method string,
	params []json.RawMessage,
) (versionedHashes []common.Hash, beaconRoot *common.Hash, requests [][]byte, err error) {
	if len(params) >= 2 {
		var hashes []common.Hash
		if err := json.Unmarshal(params[1], &hashes); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: parse blobVersionedHashes: %w", method, err)
		}

		versionedHashes = hashes
	}

	if len(params) >= 3 {
		var root common.Hash
		if err := json.Unmarshal(params[2], &root); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: parse parentBeaconBlockRoot: %w", method, err)
		}

		beaconRoot = &root
	}

	if len(params) >= 4 {
		var hexes []hexutil.Bytes
		if err := json.Unmarshal(params[3], &hexes); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: parse executionRequests: %w", method, err)
		}

		reqs := make([][]byte, len(hexes))
		for i, h := range hexes {
			reqs[i] = h
		}

		requests = reqs
	}

	return versionedHashes, beaconRoot, requests, nil
}
