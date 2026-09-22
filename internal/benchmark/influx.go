package benchmark

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// unitFields maps the units Go prints onto InfluxDB field keys. Flux queries are
// awkward to write against a key containing "/", so the common three get stable
// names and anything else is sanitized by fieldKey.
var unitFields = map[string]string{
	"ns/op":     "ns_per_op",
	"B/op":      "bytes_per_op",
	"allocs/op": "allocs_per_op",
	"MB/s":      "mb_per_sec",
}

// Point is one line of InfluxDB line protocol, ready to POST.
type Point struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]float64
	Time        time.Time
}

// NewPoints turns aggregated benchmarks into points, one per benchmark.
//
// Requires: measurement is non-empty and tags carries the run's identity
// (commit, branch). Every point shares one timestamp so a single CI run reads as
// a single moment on a graph.
func NewPoints(
	measurement string, aggregates []Aggregate, tags map[string]string, at time.Time,
) []Point {
	points := make([]Point, 0, len(aggregates))
	for _, a := range aggregates {
		fields := map[string]float64{"runs": float64(a.Runs)}
		for unit, value := range a.Metrics {
			fields[fieldKey(unit)] = value
		}

		pointTags := make(map[string]string, len(tags)+1)
		for k, v := range tags {
			if v != "" {
				pointTags[k] = v
			}
		}
		// The -N suffix is GOMAXPROCS, which changes with the runner. Keeping the
		// bare name as its own tag lets a graph follow one benchmark across
		// machines that report different suffixes.
		pointTags["name"] = a.Name
		pointTags["benchmark"] = strings.SplitN(strings.TrimPrefix(a.Name, "Benchmark"), "-", 2)[0]

		points = append(points, Point{
			Measurement: measurement,
			Tags:        pointTags,
			Fields:      fields,
			Time:        at,
		})
	}
	return points
}

// fieldKey maps a Go benchmark unit onto an InfluxDB field key.
func fieldKey(unit string) string {
	if known, ok := unitFields[unit]; ok {
		return known
	}
	// A custom b.ReportMetric unit: keep it recognizable but query-safe.
	replacer := strings.NewReplacer("/", "_per_", "-", "_", " ", "_", ".", "_", "%", "pct")
	return replacer.Replace(unit)
}

// Encode renders points as InfluxDB line protocol with nanosecond timestamps.
//
// Tag keys, tag values and field keys escape commas, equals signs and spaces,
// which is what the line protocol requires; without it a branch name containing
// a space would produce a line the server rejects or, worse, misparses.
func Encode(points []Point) string {
	var b strings.Builder
	for _, p := range points {
		b.WriteString(escapeMeasurement(p.Measurement))

		for _, k := range sortedTagKeys(p.Tags) {
			b.WriteString(",")
			b.WriteString(escapeTag(k))
			b.WriteString("=")
			b.WriteString(escapeTag(p.Tags[k]))
		}

		b.WriteString(" ")
		for i, k := range sortedFieldKeys(p.Fields) {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(escapeTag(k))
			b.WriteString("=")
			// All benchmark metrics are floats; an explicit float keeps a field
			// from changing type between runs, which InfluxDB rejects.
			fmt.Fprintf(&b, "%g", p.Fields[k])
		}

		fmt.Fprintf(&b, " %d\n", p.Time.UnixNano())
	}
	return b.String()
}

func sortedTagKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedFieldKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var (
	measurementEscaper = strings.NewReplacer(",", `\,`, " ", `\ `)
	tagEscaper         = strings.NewReplacer(",", `\,`, "=", `\=`, " ", `\ `)
)

func escapeMeasurement(s string) string { return measurementEscaper.Replace(s) }
func escapeTag(s string) string         { return tagEscaper.Replace(s) }
