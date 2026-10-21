package metrics

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func render() string {
	var b bytes.Buffer
	Write(&b)
	return b.String()
}

func TestCountersGaugesAndDurationsRenderInThePrometheusFormat(t *testing.T) {
	Reset()
	Describe("t_calls_total", "counter", "Calls.")
	Describe("t_up", "gauge", "Up.")
	Inc("t_calls_total", "result", "ok")
	Inc("t_calls_total", "result", "ok")
	Inc("t_calls_total", "result", "error")
	Add("t_calls_total", 3, "result", "ok")
	Set("t_up", 1)
	Set("t_up", 1)
	Observe("t_model", 1500*time.Millisecond, "kind", "x")
	Observe("t_model", 500*time.Millisecond, "kind", "x")
	out := render()
	for _, want := range []string{
		"# HELP t_calls_total Calls.\n# TYPE t_calls_total counter\n",
		`t_calls_total{result="error"} 1` + "\n",
		`t_calls_total{result="ok"} 5` + "\n",
		"# TYPE t_up gauge\nt_up 1\n",
		`t_model_seconds_sum{kind="x"} 2` + "\n",
		`t_model_seconds_count{kind="x"} 2` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, `result="error"`) > strings.Index(out, `result="ok"`) {
		t.Fatal("output is sorted, so it is stable")
	}
	if Get("t_calls_total", "result", "ok") != 5 {
		t.Fatal("Get")
	}
}

func TestLabelOrderDoesNotMatterAndValuesAreEscaped(t *testing.T) {
	Reset()
	Inc("t_x", "b", "2", "a", "1")
	Inc("t_x", "a", "1", "b", "2")
	if Get("t_x", "a", "1", "b", "2") != 2 {
		t.Fatal("one series")
	}
	Inc("t_y", "v", "a\"b\\c\nd")
	if !strings.Contains(render(), `t_y{v="a\"b\\c\nd"} 1`) {
		t.Fatalf("%s", render())
	}
}

func TestConcurrentUse(t *testing.T) {
	Reset()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				Inc("t_c")
				render()
			}
		}()
	}
	wg.Wait()
	if Get("t_c") != 5000 {
		t.Fatal(Get("t_c"))
	}
}
