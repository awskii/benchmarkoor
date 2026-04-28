package warmup

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeOsakaPayload returns a minimal but valid Osaka ExecutableData with no
// transactions, no withdrawals, no requests. Its BlockHash field is filled
// in to whatever the recomputed hash is, so callers that wrap it in a
// JSON-RPC envelope start from a self-consistent payload.
func makeOsakaPayload(t *testing.T, stateRoot common.Hash) (engine.ExecutableData, common.Hash, *common.Hash) {
	t.Helper()

	zero := uint64(0)
	beaconRoot := common.HexToHash("0x000102030405060708090a0b0c0d0e0f000102030405060708090a0b0c0d0e0f")
	data := engine.ExecutableData{
		ParentHash:    common.HexToHash("0x58b0689a8ff37dc82cd2840dabcd79afae585d9a261ac5658e4720a9a5ade187"),
		FeeRecipient:  common.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba"),
		StateRoot:     stateRoot,
		ReceiptsRoot:  common.HexToHash("0x9764a9b29133339b51f219a18d3bc2b617ce983cb602f95df5647fa4d7362828"),
		LogsBloom:     make([]byte, 256),
		Random:        common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Number:        100,
		GasLimit:      30_000_000,
		GasUsed:       0,
		Timestamp:     1_700_000_000,
		ExtraData:     []byte{},
		BaseFeePerGas: big.NewInt(7),
		Transactions:  [][]byte{},
		Withdrawals:   []*types.Withdrawal{}, // empty (non-nil) → withdrawalsRoot of empty trie
		BlobGasUsed:   &zero,
		ExcessBlobGas: &zero,
	}

	// Compute the canonical blockHash for this payload (with empty requests).
	block, err := engine.ExecutableDataToBlockNoHash(data, nil, &beaconRoot, [][]byte{})
	require.NoError(t, err)

	data.BlockHash = block.Hash()

	return data, block.Hash(), &beaconRoot
}

func TestNewGenerator_RejectsUnknownFork(t *testing.T) {
	_, err := NewGenerator(Fork("prague"))
	assert.Error(t, err)
}

func TestTransform_NonNewPayloadPassesThrough(t *testing.T) {
	g, err := NewGenerator(ForkOsaka)
	require.NoError(t, err)

	in := `{"jsonrpc":"2.0","id":1,"method":"engine_forkchoiceUpdatedV3","params":[]}`

	out, err := g.Transform(in)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestTransform_EmptyLinePassesThrough(t *testing.T) {
	g, err := NewGenerator(ForkOsaka)
	require.NoError(t, err)

	out, err := g.Transform("")
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestTransform_NewPayloadOverridesStateRootAndRecomputesHash(t *testing.T) {
	originalStateRoot := common.HexToHash("0xde94bab83ce96d440db7a3e2dc95ebbab73dc5aa88dc80b3d99ae8f0cff4e96c")
	data, originalHash, beaconRoot := makeOsakaPayload(t, originalStateRoot)

	payloadJSON, err := json.Marshal(&data)
	require.NoError(t, err)

	beaconRootJSON, err := json.Marshal(beaconRoot)
	require.NoError(t, err)

	emptyRequests, err := json.Marshal([]hexutil.Bytes{})
	require.NoError(t, err)

	line := `{"jsonrpc":"2.0","id":1,"method":"engine_newPayloadV4","params":[` +
		string(payloadJSON) + `,[],` + string(beaconRootJSON) + `,` + string(emptyRequests) + `]}`

	g, err := NewGenerator(ForkOsaka)
	require.NoError(t, err)

	out, err := g.Transform(line)
	require.NoError(t, err)

	// Parse the transformed line back out.
	var parsed struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "engine_newPayloadV4", parsed.Method)
	require.Len(t, parsed.Params, 4)

	var transformed engine.ExecutableData
	require.NoError(t, json.Unmarshal(parsed.Params[0], &transformed))

	// stateRoot replaced with the warmup placeholder.
	assert.Equal(t, common.HexToHash(OsakaWarmupStateRoot), transformed.StateRoot)

	// blockHash changed.
	assert.NotEqual(t, originalHash, transformed.BlockHash)

	// blockHash matches a fresh recomputation with the warmup stateRoot.
	expected, err := engine.ExecutableDataToBlockNoHash(transformed, nil, beaconRoot, [][]byte{})
	require.NoError(t, err)
	assert.Equal(t, expected.Hash(), transformed.BlockHash)
}

func TestTransformLines_PreservesOrderAndCount(t *testing.T) {
	originalStateRoot := common.HexToHash("0xde94bab83ce96d440db7a3e2dc95ebbab73dc5aa88dc80b3d99ae8f0cff4e96c")
	data, _, beaconRoot := makeOsakaPayload(t, originalStateRoot)

	payloadJSON, err := json.Marshal(&data)
	require.NoError(t, err)

	beaconRootJSON, err := json.Marshal(beaconRoot)
	require.NoError(t, err)

	emptyRequests, err := json.Marshal([]hexutil.Bytes{})
	require.NoError(t, err)

	newPayload := `{"jsonrpc":"2.0","id":1,"method":"engine_newPayloadV4","params":[` +
		string(payloadJSON) + `,[],` + string(beaconRootJSON) + `,` + string(emptyRequests) + `]}`
	fcu := `{"jsonrpc":"2.0","id":2,"method":"engine_forkchoiceUpdatedV3","params":[]}`

	g, err := NewGenerator(ForkOsaka)
	require.NoError(t, err)

	out, err := g.TransformLines([]string{newPayload, fcu})
	require.NoError(t, err)
	require.Len(t, out, 2)

	// Second line passes through unchanged.
	assert.Equal(t, fcu, out[1])

	// First line has been rewritten (different from original because stateRoot/blockHash changed).
	assert.NotEqual(t, newPayload, out[0])
}
