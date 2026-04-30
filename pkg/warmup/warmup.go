// Package warmup generates "warmup" engine_newPayload* requests by taking
// the new-payload calls from a test step, replacing the stateRoot with a
// fork-specific placeholder derived from a salt and an iteration index, and
// recomputing the blockHash so the EL client accepts the payload header
// and proceeds to execute it. The point of warmup is to populate caches
// before the real test runs.
package warmup

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Fork identifies an Ethereum hardfork. We currently only support Osaka.
type Fork string

const (
	// ForkOsaka is the only supported fork at the moment. Its header layout
	// includes withdrawalsRoot, blobGasUsed, excessBlobGas, parentBeaconRoot,
	// and requestsHash (EIP-7685).
	ForkOsaka Fork = "osaka"
)

// OsakaWarmupSalt is the 32-byte salt mixed with the iteration index to
// derive the warmup stateRoot. Picked as an arbitrary non-zero constant so
// the resulting roots are non-empty and deterministic across runs.
const OsakaWarmupSalt = "0xe8d3a308a0d3fdaeed6c196f78aad4f9620b571da6dd5b886e7fa5eba07c83e0"

// IsValidFork returns true if the given fork identifier is supported.
func IsValidFork(fork string) bool {
	return Fork(fork) == ForkOsaka
}

// Generator transforms engine_newPayload* JSON-RPC lines into warmup
// equivalents (modified stateRoot + recomputed blockHash). Each
// engine_newPayload* line expands to Count variants, each with a different
// stateRoot derived from a salt and the iteration index. Lines whose
// method is not engine_newPayload* are returned unchanged (a single copy
// regardless of Count).
type Generator struct {
	fork  Fork
	count int
	salt  []byte
}

// NewGenerator returns a Generator for the given fork. Currently only
// ForkOsaka is accepted. Count <= 0 is treated as 1.
func NewGenerator(fork Fork, count int) (*Generator, error) {
	if fork != ForkOsaka {
		return nil, fmt.Errorf("unsupported fork %q (only %q is supported)", fork, ForkOsaka)
	}

	if count <= 0 {
		count = 1
	}

	salt := common.FromHex(OsakaWarmupSalt)

	return &Generator{
		fork:  fork,
		count: count,
		salt:  salt,
	}, nil
}

// Count returns the configured number of warmup iterations per
// engine_newPayload* line.
func (g *Generator) Count() int {
	return g.count
}

// StateRootForIteration returns the deterministic stateRoot used for
// warmup iteration i. It is exported primarily for tests.
func (g *Generator) StateRootForIteration(i int) common.Hash {
	buf := make([]byte, 0, len(g.salt)+8)
	buf = append(buf, g.salt...)

	var ibe [8]byte
	binary.BigEndian.PutUint64(ibe[:], uint64(i)) //nolint:gosec // i is non-negative.
	buf = append(buf, ibe[:]...)

	return common.BytesToHash(crypto.Keccak256(buf))
}

// Transform rewrites a single JSON-RPC line. Non-engine_newPayload* lines
// pass through unchanged (a single-element slice). For engine_newPayload*
// lines, returns Count variants, each with its iteration's derived
// stateRoot and a recomputed blockHash. Empty/whitespace lines pass
// through unchanged.
func (g *Generator) Transform(line string) ([]string, error) {
	if strings.TrimSpace(line) == "" {
		return []string{line}, nil
	}

	var raw struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      json.RawMessage   `json:"id"`
		Method  string            `json:"method"`
		Params  []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, fmt.Errorf("parse jsonrpc line: %w", err)
	}

	if !strings.HasPrefix(raw.Method, "engine_newPayload") {
		return []string{line}, nil
	}

	if len(raw.Params) < 1 {
		return nil, fmt.Errorf("%s: expected at least 1 param, got %d", raw.Method, len(raw.Params))
	}

	var data engine.ExecutableData
	if err := json.Unmarshal(raw.Params[0], &data); err != nil {
		return nil, fmt.Errorf("parse executionPayload: %w", err)
	}

	// engine_newPayloadV3+ carry blobVersionedHashes (params[1]),
	// parentBeaconBlockRoot (params[2]) and (V4+) executionRequests (params[3]).
	versionedHashes, beaconRoot, requests, err := decodeExtraParams(raw.Method, raw.Params)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, g.count)

	for i := range g.count {
		// Replace stateRoot for this iteration and recompute blockHash.
		// ExecutableDataToBlockNoHash builds a block with all derived roots
		// (txRoot, withdrawalsRoot, requestsHash) without verifying the
		// supplied blockHash matches.
		variant := data
		variant.StateRoot = g.StateRootForIteration(i)

		block, err := engine.ExecutableDataToBlockNoHash(variant, versionedHashes, beaconRoot, requests)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: build block from payload: %w", i, err)
		}

		variant.BlockHash = block.Hash()

		newPayload, err := json.Marshal(&variant)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: marshal warmup payload: %w", i, err)
		}

		// Clone params so each line gets its own params[0]. The other
		// params (blobVersionedHashes, beaconRoot, requests) are shared
		// raw bytes — safe to alias.
		params := make([]json.RawMessage, len(raw.Params))
		copy(params, raw.Params)
		params[0] = newPayload

		envelope := struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      json.RawMessage   `json:"id"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
		}{
			JSONRPC: raw.JSONRPC,
			ID:      raw.ID,
			Method:  raw.Method,
			Params:  params,
		}

		encoded, err := json.Marshal(&envelope)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: marshal jsonrpc line: %w", i, err)
		}

		out = append(out, string(encoded))
	}

	return out, nil
}

// TransformLines applies Transform to every line in the input slice. The
// returned slice may be longer than the input: each engine_newPayload*
// line expands to Count variants while non-newPayload lines pass through
// once.
func (g *Generator) TransformLines(lines []string) ([]string, error) {
	out := make([]string, 0, len(lines)*g.count)

	for i, line := range lines {
		transformed, err := g.Transform(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}

		out = append(out, transformed...)
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
