// Package benchmark turns `go test -bench` output into points a time-series
// database can store, so a performance trend is visible across commits rather
// than only inside one CI log.
//
// This file is the functional core: parsing and aggregation with no I/O. The
// shell that reads a file and writes to InfluxDB lives in cmd/benchpublish.
package benchmark

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Sample is one benchmark line: a single measurement of one benchmark.
//
// Metrics is keyed by the unit Go printed ("ns/op", "B/op", "allocs/op", or
// anything b.ReportMetric added), so a benchmark reporting a custom metric is
// carried through rather than dropped.
type Sample struct {
	Name       string
	Iterations int64
	Metrics    map[string]float64
}

// Aggregate is every Sample for one benchmark reduced to a single point.
//
// The reduction is the median, not the mean: `go test -count=N` on a shared CI
// runner produces occasional outliers an order of magnitude slow, and one such
// sample drags a mean far enough to look like a regression.
type Aggregate struct {
	Name    string
	Runs    int
	Metrics map[string]float64
}

// ParseSamples reads `go test -bench` output and returns one Sample per
// benchmark line, in the order they appeared.
//
// Lines that are not benchmark results are ignored, which covers the goos/goarch
// preamble, PASS, and the ok summary. A line that starts with "Benchmark" but
// cannot be parsed is an error rather than a skip: silently dropping a result
// would report a partial run as a complete one.
func ParseSamples(r io.Reader) ([]Sample, error) {
	var samples []Sample
	scanner := bufio.NewScanner(r)
	// Benchmark lines are short, but a custom metric set could exceed the default
	// 64KiB token; grow the ceiling rather than fail on a long line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 0; scanner.Scan(); {
		line++
		text := scanner.Text()
		if !strings.HasPrefix(text, "Benchmark") {
			continue
		}
		sample, err := parseSample(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading benchmark output: %w", err)
	}
	return samples, nil
}

// parseSample reads one line of the form:
//
//	BenchmarkToProtoPet-10   6231312   207.6 ns/op   416 B/op   5 allocs/op
//
// The name and iteration count are fixed; everything after is value/unit pairs,
// so a benchmark using b.ReportMetric parses without this needing to know the
// metric.
func parseSample(line string) (Sample, error) {
	fields := strings.Fields(line)
	// A name, an iteration count, and at least one value/unit pair.
	const minFields = 4
	if len(fields) < minFields {
		return Sample{}, fmt.Errorf("expected at least %d fields, got %d: %q", minFields, len(fields), line)
	}

	iterations, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Sample{}, fmt.Errorf("iteration count %q: %w", fields[1], err)
	}

	rest := fields[2:]
	if len(rest)%2 != 0 {
		return Sample{}, fmt.Errorf("trailing metric without a unit: %q", line)
	}
	metrics := make(map[string]float64, len(rest)/2)
	for i := 0; i < len(rest); i += 2 {
		value, parseErr := strconv.ParseFloat(rest[i], 64)
		if parseErr != nil {
			return Sample{}, fmt.Errorf("metric value %q: %w", rest[i], parseErr)
		}
		metrics[rest[i+1]] = value
	}

	return Sample{Name: fields[0], Iterations: iterations, Metrics: metrics}, nil
}

// Aggregate reduces repeated samples of the same benchmark to one median point
// each, sorted by name so the output is stable.
//
// Writing one point per sample instead would be wrong: points sharing a
// measurement, tag set and timestamp overwrite each other in InfluxDB, so a
// -count=10 run would silently keep whichever sample was written last.
func AggregateSamples(samples []Sample) []Aggregate {
	order := make([]string, 0, len(samples))
	byName := make(map[string][]Sample, len(samples))
	for _, s := range samples {
		if _, seen := byName[s.Name]; !seen {
			order = append(order, s.Name)
		}
		byName[s.Name] = append(byName[s.Name], s)
	}
	sort.Strings(order)

	aggregates := make([]Aggregate, 0, len(order))
	for _, name := range order {
		group := byName[name]
		units := map[string]bool{}
		for _, s := range group {
			for unit := range s.Metrics {
				units[unit] = true
			}
		}
		medians := make(map[string]float64, len(units))
		for unit := range units {
			values := make([]float64, 0, len(group))
			for _, s := range group {
				if v, ok := s.Metrics[unit]; ok {
					values = append(values, v)
				}
			}
			medians[unit] = median(values)
		}
		aggregates = append(aggregates, Aggregate{Name: name, Runs: len(group), Metrics: medians})
	}
	return aggregates
}

// median returns the middle value, averaging the two middle values for an even
// count. It sorts a copy, so the caller's slice is untouched.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
