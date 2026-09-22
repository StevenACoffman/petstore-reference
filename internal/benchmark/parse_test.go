package benchmark_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/benchmark"
)

// realOutput is a verbatim `go test -bench=. -benchmem` run, preamble and all,
// so the parser is tested against what the tool actually emits.
const realOutput = `goos: darwin
goarch: arm64
pkg: github.com/example/pets/internal/pet
cpu: Apple M1 Pro
BenchmarkNewPetInput-10        	11374406	        89.91 ns/op	       0 B/op	       0 allocs/op
BenchmarkToProtoPet-10         	 6231312	       207.6 ns/op	     416 B/op	       5 allocs/op
PASS
ok  	github.com/example/pets/internal/pet	9.111s
`

func TestParseSamplesReadsRealOutput(t *testing.T) {
	t.Parallel()

	samples, err := benchmark.ParseSamples(strings.NewReader(realOutput))

	require.NoError(t, err)
	require.Len(t, samples, 2, "the preamble, PASS and ok lines must be ignored")
	assert.Equal(t, "BenchmarkNewPetInput-10", samples[0].Name)
	assert.Equal(t, int64(11374406), samples[0].Iterations)
	assert.InDelta(t, 89.91, samples[0].Metrics["ns/op"], 0.001)
	assert.InDelta(t, 416, samples[1].Metrics["B/op"], 0.001)
	assert.InDelta(t, 5, samples[1].Metrics["allocs/op"], 0.001)
}

// TestParseSamplesKeepsCustomMetrics is why this parses fields instead of
// matching a fixed ns/B/allocs regex: b.ReportMetric output would otherwise be
// silently discarded.
func TestParseSamplesKeepsCustomMetrics(t *testing.T) {
	t.Parallel()

	line := "BenchmarkList-8\t100\t1234 ns/op\t7.5 rows/op\n"

	samples, err := benchmark.ParseSamples(strings.NewReader(line))

	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.InDelta(t, 7.5, samples[0].Metrics["rows/op"], 0.001)
}

// TestParseSamplesWithoutBenchmem covers a run that omits -benchmem, which emits
// only ns/op.
func TestParseSamplesWithoutBenchmem(t *testing.T) {
	t.Parallel()

	samples, err := benchmark.ParseSamples(strings.NewReader("BenchmarkX-4\t500\t3.21 ns/op\n"))

	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Len(t, samples[0].Metrics, 1)
}

// TestParseSamplesRejectsMalformedLines pins the deliberate choice to fail
// rather than skip: a dropped line would report a partial run as a whole one.
func TestParseSamplesRejectsMalformedLines(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"iteration count is not a number": "BenchmarkX-4\tmany\t3.21 ns/op\n",
		"metric without a unit":           "BenchmarkX-4\t500\t3.21 ns/op\t99\n",
		"metric value is not a number":    "BenchmarkX-4\t500\tfast ns/op\n",
		"too few fields":                  "BenchmarkX-4\t500\n",
	}

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := benchmark.ParseSamples(strings.NewReader(line))
			require.Error(t, err)
		})
	}
}

// TestAggregateSamplesTakesTheMedian is the outlier defence: a shared CI runner
// stalls occasionally, and a mean would turn one stall into a reported
// regression.
func TestAggregateSamplesTakesTheMedian(t *testing.T) {
	t.Parallel()

	samples := []benchmark.Sample{
		{Name: "BenchmarkX-8", Metrics: map[string]float64{"ns/op": 100}},
		{Name: "BenchmarkX-8", Metrics: map[string]float64{"ns/op": 102}},
		{Name: "BenchmarkX-8", Metrics: map[string]float64{"ns/op": 9000}}, // a stall
	}

	got := benchmark.AggregateSamples(samples)

	require.Len(t, got, 1)
	assert.Equal(t, 3, got[0].Runs)
	assert.InDelta(t, 102, got[0].Metrics["ns/op"], 0.001,
		"the median must ignore the outlier a mean would follow")
}

func TestAggregateSamplesAveragesTheMiddlePairWhenEven(t *testing.T) {
	t.Parallel()

	samples := []benchmark.Sample{
		{Name: "BenchmarkX-8", Metrics: map[string]float64{"ns/op": 10}},
		{Name: "BenchmarkX-8", Metrics: map[string]float64{"ns/op": 20}},
	}

	got := benchmark.AggregateSamples(samples)

	require.Len(t, got, 1)
	assert.InDelta(t, 15, got[0].Metrics["ns/op"], 0.001)
}

func TestAggregateSamplesGroupsAndSorts(t *testing.T) {
	t.Parallel()

	samples := []benchmark.Sample{
		{Name: "BenchmarkZ-8", Metrics: map[string]float64{"ns/op": 3}},
		{Name: "BenchmarkA-8", Metrics: map[string]float64{"ns/op": 1}},
		{Name: "BenchmarkZ-8", Metrics: map[string]float64{"ns/op": 5}},
	}

	got := benchmark.AggregateSamples(samples)

	require.Len(t, got, 2)
	assert.Equal(t, "BenchmarkA-8", got[0].Name, "output must be sorted for a stable diff")
	assert.Equal(t, "BenchmarkZ-8", got[1].Name)
	assert.Equal(t, 2, got[1].Runs)
}

func TestAggregateSamplesOfNothing(t *testing.T) {
	t.Parallel()

	assert.Empty(t, benchmark.AggregateSamples(nil))
}

func TestEncodeProducesLineProtocol(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	points := benchmark.NewPoints("benchmark",
		[]benchmark.Aggregate{{
			Name: "BenchmarkToProtoPet-10",
			Runs: 10,
			Metrics: map[string]float64{
				"ns/op": 207.6, "B/op": 416, "allocs/op": 5,
			},
		}},
		map[string]string{"branch": "main", "commit": "abc1234"},
		at,
	)

	line := benchmark.Encode(points)

	assert.Contains(t, line, "benchmark,")
	assert.Contains(t, line, "branch=main")
	assert.Contains(t, line, "commit=abc1234")
	assert.Contains(t, line, "name=BenchmarkToProtoPet-10")
	assert.Contains(t, line, "benchmark=ToProtoPet", "the bare name survives a GOMAXPROCS change")
	assert.Contains(t, line, "ns_per_op=207.6")
	assert.Contains(t, line, "bytes_per_op=416")
	assert.Contains(t, line, "allocs_per_op=5")
	assert.Contains(t, line, "runs=10")
	assert.True(t, strings.HasSuffix(line, " 1790071200000000000\n"), "got %q", line)
}

// TestEncodeEscapesSeparators is the reason encoding is not fmt.Sprintf: a
// branch name with a space or comma would otherwise emit a line the server
// misparses.
func TestEncodeEscapesSeparators(t *testing.T) {
	t.Parallel()

	points := benchmark.NewPoints("bench marks",
		[]benchmark.Aggregate{{Name: "BenchmarkX-8", Runs: 1, Metrics: map[string]float64{"ns/op": 1}}},
		map[string]string{"branch": "feature/a b,c", "tricky": "k=v"},
		time.Unix(0, 0),
	)

	line := benchmark.Encode(points)

	assert.Contains(t, line, `bench\ marks`)
	assert.Contains(t, line, `branch=feature/a\ b\,c`)
	assert.Contains(t, line, `tricky=k\=v`)
}

func TestNewPointsDropsEmptyTags(t *testing.T) {
	t.Parallel()

	points := benchmark.NewPoints("benchmark",
		[]benchmark.Aggregate{{Name: "BenchmarkX-8", Runs: 1, Metrics: map[string]float64{"ns/op": 1}}},
		map[string]string{"branch": "main", "commit": ""},
		time.Unix(0, 0),
	)

	require.Len(t, points, 1)
	assert.NotContains(t, points[0].Tags, "commit",
		"an empty tag value is rejected by InfluxDB, so it must not be sent")
}

func TestFieldKeySanitizesCustomUnits(t *testing.T) {
	t.Parallel()

	points := benchmark.NewPoints("benchmark",
		[]benchmark.Aggregate{{Name: "BenchmarkX-8", Metrics: map[string]float64{"rows/op": 7}}},
		nil, time.Unix(0, 0),
	)

	require.Len(t, points, 1)
	assert.Contains(t, points[0].Fields, "rows_per_op")
}
