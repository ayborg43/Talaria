// Package metrics keeps a few counters for the process and writes them in the
// Prometheus text format, so a running gateway can be watched and alerted on
// with ordinary tools. It records counts and durations only: never message text,
// arguments or anything a user wrote.
package metrics

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

type series struct {
	name   string
	labels string // `{a="b",c="d"}` or ""
}

type registry struct {
	mu     sync.Mutex
	values map[series]float64
	help   map[string]string
	kind   map[string]string
}

var std = &registry{values: map[series]float64{}, help: map[string]string{}, kind: map[string]string{}}

// Describe says what a metric is, once, for its # HELP and # TYPE lines.
// kind is "counter" or "gauge".
func Describe(name, kind, help string) {
	std.mu.Lock()
	std.help[name], std.kind[name] = help, kind
	std.mu.Unlock()
}

func labelString(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, kv[i], escape(kv[i+1])))
	}
	slices.Sort(pairs)
	return "{" + strings.Join(pairs, ",") + "}"
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

// Add adds v to the counter name with the given label pairs (key, value, ...).
func Add(name string, v float64, labels ...string) {
	std.mu.Lock()
	std.values[series{name, labelString(labels)}] += v
	std.mu.Unlock()
}

// Inc adds one.
func Inc(name string, labels ...string) { Add(name, 1, labels...) }

// Set sets a gauge.
func Set(name string, v float64, labels ...string) {
	std.mu.Lock()
	std.values[series{name, labelString(labels)}] = v
	std.mu.Unlock()
}

// Observe records a duration as name_seconds_sum and name_seconds_count.
func Observe(name string, d time.Duration, labels ...string) {
	Add(name+"_seconds_sum", d.Seconds(), labels...)
	Inc(name+"_seconds_count", labels...)
}

// Get returns a series' value, for tests.
func Get(name string, labels ...string) float64 {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.values[series{name, labelString(labels)}]
}

// Reset clears every value, for tests.
func Reset() {
	std.mu.Lock()
	std.values = map[series]float64{}
	std.mu.Unlock()
}

// Write renders everything in the Prometheus text format, sorted.
func Write(w io.Writer) {
	std.mu.Lock()
	defer std.mu.Unlock()
	all := make([]series, 0, len(std.values))
	for s := range std.values {
		all = append(all, s)
	}
	slices.SortFunc(all, func(a, b series) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.labels, b.labels))
	})
	last := ""
	for _, s := range all {
		base := strings.TrimSuffix(strings.TrimSuffix(s.name, "_seconds_sum"), "_seconds_count")
		if s.name != last {
			if h, ok := std.help[s.name]; ok {
				fmt.Fprintf(w, "# HELP %s %s\n", s.name, h)
			} else if h, ok := std.help[base]; ok && base != s.name {
				fmt.Fprintf(w, "# HELP %s %s\n", s.name, h)
			}
			if k, ok := std.kind[s.name]; ok {
				fmt.Fprintf(w, "# TYPE %s %s\n", s.name, k)
			} else if _, ok := std.kind[base]; ok && base != s.name {
				fmt.Fprintf(w, "# TYPE %s counter\n", s.name)
			}
			last = s.name
		}
		fmt.Fprintf(w, "%s%s %g\n", s.name, s.labels, std.values[s])
	}
}
