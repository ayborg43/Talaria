package sse

import (
	"reflect"
	"strings"
	"testing"
)

func collect(t *testing.T, input string) []string {
	t.Helper()
	var got []string
	if err := Read(strings.NewReader(input), func(d string) error {
		got = append(got, d)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReadSSE(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"basic":                          {"data: one\n\ndata: two\n\n", []string{"one", "two"}},
		"crlf":                           {"data: one\r\n\r\ndata: two\r\n\r\n", []string{"one", "two"}},
		"no space":                       {"data:one\n\n", []string{"one"}},
		"event line ignored":             {"event: ping\ndata: {\"a\":1}\n\n", []string{`{"a":1}`}},
		"comment ignored":                {": keep-alive\n\ndata: x\n\n", []string{"x"}},
		"multi-line data":                {"data: a\ndata: b\n\n", []string{"a\nb"}},
		"final event without blank line": {"data: last", []string{"last"}},
		"empty":                          {"", nil},
	}
	for name, c := range cases {
		if got := collect(t, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestReadSSEStopAndErrors(t *testing.T) {
	var n int
	err := Read(strings.NewReader("data: 1\n\ndata: 2\n\ndata: 3\n\n"), func(string) error {
		n++
		if n == 2 {
			return Stop
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Fatalf("stop: err=%v n=%d", err, n)
	}

	big := strings.Repeat("x", 200_000) // longer than bufio.Scanner's default token limit
	if got := collect(t, "data: "+big+"\n\n"); len(got) != 1 || len(got[0]) != len(big) {
		t.Fatal("large event was not read intact")
	}
}
