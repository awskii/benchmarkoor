package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethpandaops/benchmarkoor/pkg/eest"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inlineProvider satisfies the StepProvider interface for tests.
type inlineProvider struct {
	lines []string
}

func (p *inlineProvider) Lines() []string { return p.lines }
func (p *inlineProvider) Content() []byte { return []byte(joinLines(p.lines)) }

func joinLines(ls []string) string {
	return strings.Join(ls, "\n") + "\n"
}

func TestCreateSuiteOutput_WritesPayloadSizes(t *testing.T) {
	tmp := t.TempDir()
	// One test step with one engine_newPayloadV3 line.
	testLine := minimalDenebRequest(t)
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_payload_sizes",
				Test: &StepFile{
					Name:     "test_payload_sizes",
					Provider: &inlineProvider{lines: []string{testLine}},
				},
			},
		},
	}
	info := &SuiteInfo{
		Hash: "deadbeef",
	}
	log := logrus.New()
	err := CreateSuiteOutput(log, tmp, "deadbeef", info, prepared, nil, nil)
	require.NoError(t, err)

	summaryPath := filepath.Join(tmp, "suites", "deadbeef", "summary.json")
	data, err := os.ReadFile(summaryPath)
	require.NoError(t, err)

	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Len(t, parsed.Tests, 1)
	require.NotNil(t, parsed.Tests[0].PayloadSizes)
	require.NotNil(t, parsed.Tests[0].PayloadSizes.Test)
	tps := parsed.Tests[0].PayloadSizes.Test
	require.Len(t, tps.SSZFull, 1)
	require.Len(t, tps.SSZFullSnappy, 1)
	assert.Greater(t, tps.SSZFull[0], uint64(100))
	assert.Greater(t, tps.SSZFullSnappy[0], uint64(0))
	assert.LessOrEqual(t, tps.SSZFullSnappy[0], tps.SSZFull[0])
}

func TestCreateSuiteOutput_AggregatesMetadataOpcodeCounts(t *testing.T) {
	tmp := t.TempDir()
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_opcode_counts",
				EESTInfo: &eest.FixtureInfo{
					FixtureFormat: eest.SupportedStatefulFixtureFormat,
					Metadata: &eest.FixtureMetadata{
						OpcodeCounts: []map[string]int{
							{"PUSH1": 3, "ADD": 1},
							nil,
							{"PUSH1": 2, "MUL": 4},
						},
					},
				},
			},
		},
	}
	info := &SuiteInfo{Hash: "cafe"}
	err := CreateSuiteOutput(logrus.New(), tmp, "cafe", info, prepared, nil, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmp, "suites", "cafe", "summary.json"))
	require.NoError(t, err)

	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Len(t, parsed.Tests, 1)
	assert.Equal(t,
		map[string]int{"PUSH1": 5, "ADD": 1, "MUL": 4},
		parsed.Tests[0].OpcodeCount,
	)
	// The raw per-payload counts stay available under eest.info.metadata.
	require.NotNil(t, parsed.Tests[0].EEST)
	require.NotNil(t, parsed.Tests[0].EEST.Info.Metadata)
	assert.Len(t, parsed.Tests[0].EEST.Info.Metadata.OpcodeCounts, 3)
}

func TestMergeOpcodeData_UsesMetadataOpcodeCounts(t *testing.T) {
	existing := []SuiteTest{{Name: "test_a"}, {Name: "test_b"}}
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_a",
				EESTInfo: &eest.FixtureInfo{
					Metadata: &eest.FixtureMetadata{
						OpcodeCounts: []map[string]int{{"PUSH1": 2}, {"PUSH1": 1}},
					},
				},
			},
			{
				Name:     "test_b",
				EESTInfo: &eest.FixtureInfo{OpcodeCount: map[string]int{"ADD": 7}},
			},
		},
	}

	mergeOpcodeData(existing, prepared)

	assert.Equal(t, map[string]int{"PUSH1": 3}, existing[0].OpcodeCount)
	assert.Equal(t, map[string]int{"ADD": 7}, existing[1].OpcodeCount)
}

func TestSuiteInfo_BackwardCompat_LoadsOldSummary(t *testing.T) {
	// Construct an old-style summary.json (no payload-size fields) and verify it parses.
	old := `{
		"hash": "f00d",
		"tests": [{"name": "test_old", "opcode_count": {"PUSH1": 3}}]
	}`
	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal([]byte(old), &parsed))
	require.Len(t, parsed.Tests, 1)
	assert.Equal(t, "test_old", parsed.Tests[0].Name)
	assert.Nil(t, parsed.Tests[0].PayloadSizes)
}

func TestCreateSuiteOutput_CopiesEESTMeta(t *testing.T) {
	tmp := t.TempDir()

	// Build a source .meta dir with a top-level file and a nested file.
	metaDir := filepath.Join(tmp, "fixtures", ".meta")
	require.NoError(t, os.MkdirAll(filepath.Join(metaDir, "assets"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(metaDir, "fixtures.ini"),
		[]byte("[environment]\npython = 3.12.13\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(metaDir, "assets", "style.css"), []byte("body{}"), 0o644))

	prepared := &PreparedSource{
		MetaDir: metaDir,
		Tests: []*TestWithSteps{
			{
				Name: "test_meta",
				Test: &StepFile{
					Name:     "test_meta",
					Provider: &inlineProvider{lines: []string{minimalDenebRequest(t)}},
				},
			},
		},
	}
	info := &SuiteInfo{Hash: "abc123"}

	require.NoError(t, CreateSuiteOutput(logrus.New(), tmp, "abc123", info, prepared, nil, nil))

	suiteMeta := filepath.Join(tmp, "suites", "abc123", ".eest-meta")

	gotIni, err := os.ReadFile(filepath.Join(suiteMeta, "fixtures.ini"))
	require.NoError(t, err)
	assert.Contains(t, string(gotIni), "python = 3.12.13")

	gotCSS, err := os.ReadFile(filepath.Join(suiteMeta, "assets", "style.css"))
	require.NoError(t, err)
	assert.Equal(t, "body{}", string(gotCSS))

	// summary.json flags the metadata so the UI can surface it.
	data, err := os.ReadFile(filepath.Join(tmp, "suites", "abc123", "summary.json"))
	require.NoError(t, err)

	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.True(t, parsed.EESTMetadata)
}

func TestCreateSuiteOutput_NoEESTMetaWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_nometa",
				Test: &StepFile{
					Name:     "test_nometa",
					Provider: &inlineProvider{lines: []string{minimalDenebRequest(t)}},
				},
			},
		},
	}
	info := &SuiteInfo{Hash: "nometa01"}

	require.NoError(t, CreateSuiteOutput(logrus.New(), tmp, "nometa01", info, prepared, nil, nil))

	_, err := os.Stat(filepath.Join(tmp, "suites", "nometa01", ".eest-meta"))
	assert.True(t, os.IsNotExist(err))

	data, err := os.ReadFile(filepath.Join(tmp, "suites", "nometa01", "summary.json"))
	require.NoError(t, err)

	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.False(t, parsed.EESTMetadata)
}

func TestCreateSuiteOutput_MergesPayloadSizesOnSecondRun(t *testing.T) {
	tmp := t.TempDir()
	testLine := minimalDenebRequest(t)
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_payload_merge",
				Test: &StepFile{
					Name:     "test_payload_merge",
					Provider: &inlineProvider{lines: []string{testLine}},
				},
			},
		},
	}

	// First run — creates the suite and writes initial sizes.
	log := logrus.New()
	info1 := &SuiteInfo{Hash: "cafef00d"}
	require.NoError(t, CreateSuiteOutput(log, tmp, "cafef00d", info1, prepared, nil, nil))

	// Simulate a legacy summary: rewrite the file with payload_sizes cleared.
	summaryPath := filepath.Join(tmp, "suites", "cafef00d", "summary.json")
	data, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var legacy SuiteInfo
	require.NoError(t, json.Unmarshal(data, &legacy))
	for i := range legacy.Tests {
		legacy.Tests[i].PayloadSizes = nil
	}
	zeroed, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(summaryPath, zeroed, 0644))

	// Second run — should detect suite exists, read on-disk test.request, and merge.
	info2 := &SuiteInfo{Hash: "cafef00d"}
	require.NoError(t, CreateSuiteOutput(log, tmp, "cafef00d", info2, prepared, nil, nil))

	final, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(final, &parsed))
	require.Len(t, parsed.Tests, 1)
	require.NotNil(t, parsed.Tests[0].PayloadSizes)
	require.NotNil(t, parsed.Tests[0].PayloadSizes.Test)
	require.Len(t, parsed.Tests[0].PayloadSizes.Test.SSZFull, 1)
	assert.Greater(t, parsed.Tests[0].PayloadSizes.Test.SSZFull[0], uint64(100), "merge path should backfill sizes")
}

// A CI worker starts each job with an empty results directory, so it has no
// local suite to merge into. Without the stored summary it would rebuild the
// description from its own inputs and overwrite what earlier, richer runs
// contributed — opcode counts from an external source being the clearest case,
// since a run without that config cannot recompute them.
func TestCreateSuiteOutput_MergesStoredSummaryOnWipedWorker(t *testing.T) {
	log := logrus.New()
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_opcode_merge",
				Test: &StepFile{
					Name:     "test_opcode_merge",
					Provider: &inlineProvider{lines: []string{minimalDenebRequest(t)}},
				},
			},
		},
	}

	// A previous run, on some other worker, recorded opcode counts.
	stored, err := json.Marshal(&SuiteInfo{
		Hash: "beefcafe",
		Tests: []SuiteTest{{
			Name:        "test_opcode_merge",
			OpcodeCount: map[string]int{"PUSH1": 42},
		}},
	})
	require.NoError(t, err)

	// This run gets a fresh results dir and no opcode source of its own.
	tmp := t.TempDir()
	info := &SuiteInfo{Hash: "beefcafe"}
	require.NoError(t, CreateSuiteOutput(log, tmp, "beefcafe", info, prepared, nil, stored))

	summaryPath := filepath.Join(tmp, "suites", "beefcafe", "summary.json")
	data, err := os.ReadFile(summaryPath)
	require.NoError(t, err)

	var parsed SuiteInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Len(t, parsed.Tests, 1)
	assert.Equal(t, map[string]int{"PUSH1": 42}, parsed.Tests[0].OpcodeCount,
		"stored opcode counts must survive a run that cannot recompute them")

	// The step files are still materialised: a stored summary says nothing
	// about what is on local disk, and the upload has to have bytes to send.
	assert.FileExists(t, filepath.Join(tmp, "suites", "beefcafe", "test_opcode_merge", "test.request"))

	// And the merge still backfills from those materialised files.
	require.NotNil(t, parsed.Tests[0].PayloadSizes)
	assert.NotNil(t, parsed.Tests[0].PayloadSizes.Test)
}

// A truncated or test-less stored summary must not be merged into — that is
// what leaves "tests": null behind.
func TestCreateSuiteOutput_IgnoresUnusableStoredSummary(t *testing.T) {
	log := logrus.New()
	prepared := &PreparedSource{
		Tests: []*TestWithSteps{
			{
				Name: "test_ignore_bad",
				Test: &StepFile{
					Name:     "test_ignore_bad",
					Provider: &inlineProvider{lines: []string{minimalDenebRequest(t)}},
				},
			},
		},
	}

	for name, stored := range map[string][]byte{
		"truncated": []byte(`{"hash":"d00d","tests":`),
		"no tests":  []byte(`{"hash":"d00d"}`),
		"empty":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			info := &SuiteInfo{Hash: "d00d"}
			require.NoError(t, CreateSuiteOutput(log, tmp, "d00d", info, prepared, nil, stored))

			data, err := os.ReadFile(filepath.Join(tmp, "suites", "d00d", "summary.json"))
			require.NoError(t, err)

			var parsed SuiteInfo
			require.NoError(t, json.Unmarshal(data, &parsed))
			require.Len(t, parsed.Tests, 1, "falls back to this run's own description")
			assert.Equal(t, "test_ignore_bad", parsed.Tests[0].Name)
		})
	}
}
