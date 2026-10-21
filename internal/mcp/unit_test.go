package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func envMap(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// The expected values are the examples in the specification's value-encoding table.
func TestEncodeHeaderValueMatchesTheSpecTable(t *testing.T) {
	for in, want := range map[string]string{
		"us-west1":             "us-west1",
		"Hello, 世界":            "=?base64?SGVsbG8sIOS4lueVjA==?=",
		" padded ":             "=?base64?IHBhZGRlZCA=?=",
		"line1\nline2":         "=?base64?bGluZTEKbGluZTI=?=",
		"=?base64?literal?=":   "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=",
		"tab\there":            "=?base64?dGFiCWhlcmU=?=",
		"":                     "",
		"has spaces inside ok": "has spaces inside ok",
	} {
		if got := encodeHeaderValue(in); got != want {
			t.Errorf("encodeHeaderValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMirroredParams(t *testing.T) {
	good := json.RawMessage(`{"type":"object","properties":{
		"region":{"type":"string","x-mcp-header":"Region"},
		"opts":{"type":"object","properties":{"dry":{"type":"boolean","x-mcp-header":"Dry-Run"},"n":{"type":"integer","x-mcp-header":"Count"}}},
		"free":{"type":"string"}}}`)
	got, err := mirroredParams(good)
	if err != nil {
		t.Fatal(err)
	}
	want := []paramHeader{
		{"Region", []string{"region"}},
		{"Dry-Run", []string{"opts", "dry"}},
		{"Count", []string{"opts", "n"}},
	}
	byName := map[string][]string{}
	for _, p := range got {
		byName[p.Name] = p.Path
	}
	for _, w := range want {
		if !reflect.DeepEqual(byName[w.Name], w.Path) {
			t.Errorf("%s: path %v, want %v", w.Name, byName[w.Name], w.Path)
		}
	}

	if got, err := mirroredParams(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)); got != nil || err != nil {
		t.Fatalf("no annotations: %v %v", got, err)
	}

	for name, schema := range map[string]string{
		"number type":      `{"type":"object","properties":{"n":{"type":"number","x-mcp-header":"N"}}}`,
		"array type":       `{"type":"object","properties":{"n":{"type":"array","x-mcp-header":"N"}}}`,
		"empty name":       `{"type":"object","properties":{"n":{"type":"string","x-mcp-header":""}}}`,
		"space in name":    `{"type":"object","properties":{"n":{"type":"string","x-mcp-header":"My Header"}}}`,
		"non-string name":  `{"type":"object","properties":{"n":{"type":"string","x-mcp-header":5}}}`,
		"duplicate (case)": `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"X"},"b":{"type":"string","x-mcp-header":"x"}}}`,
		"inside items":     `{"type":"object","properties":{"l":{"type":"array","items":{"type":"object","properties":{"i":{"type":"string","x-mcp-header":"I"}}}}}}`,
		"inside oneOf":     `{"type":"object","properties":{"o":{"oneOf":[{"type":"string","x-mcp-header":"O"}]}}}`,
	} {
		if _, err := mirroredParams(json.RawMessage(schema)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestMirroredHeaders(t *testing.T) {
	params := []paramHeader{
		{"S", []string{"s"}}, {"B", []string{"b"}}, {"I", []string{"i"}}, {"F", []string{"f"}},
		{"Deep", []string{"o", "x"}}, {"Missing", []string{"nope"}}, {"Null", []string{"nul"}}, {"Big", []string{"big"}},
	}
	dec := json.NewDecoder(strings.NewReader(`{"s":"héllo","b":true,"i":42,"f":7.0,"o":{"x":"deep"},"nul":null,"big":9007199254740993}`))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		t.Fatal(err)
	}

	got := mirroredHeaders(params, args)
	want := map[string]string{
		"Mcp-Param-S":    "=?base64?aMOpbGxv?=",
		"Mcp-Param-B":    "true",
		"Mcp-Param-I":    "42",
		"Mcp-Param-F":    "7",
		"Mcp-Param-Deep": "deep",
		"Mcp-Param-Big":  "9007199254740993", // exact: integers are not squeezed through float64
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if mirroredHeaders(nil, args) != nil {
		t.Fatal("no mirrored parameters, no headers")
	}
}

func TestServerValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		name string
		s    Server
		want string // substring of the error; "" means valid
	}{
		"stdio":          {"fs", Server{Command: "npx", Args: []string{"-y", "x"}}, ""},
		"http":           {"remote", Server{URL: "https://example.com/mcp", Headers: map[string]string{"A": "b"}}, ""},
		"url with var":   {"remote", Server{URL: "https://${HOST}/mcp"}, ""},
		"both":           {"x", Server{Command: "a", URL: "http://h"}, "not both"},
		"neither":        {"x", Server{}, "command (stdio) or a url"},
		"bad name":       {"has space", Server{Command: "a"}, "server name"},
		"long name":      {strings.Repeat("a", 33), Server{Command: "a"}, "server name"},
		"bad scheme":     {"x", Server{URL: "ftp://h/x"}, "http:// or https://"},
		"no host":        {"x", Server{URL: "http://"}, "http:// or https://"},
		"args on url":    {"x", Server{URL: "http://h", Args: []string{"a"}}, "only apply to a command"},
		"headers on cmd": {"x", Server{Command: "a", Headers: map[string]string{"A": "b"}}, "only apply to a url"},
		"negative":       {"x", Server{Command: "a", TimeoutSeconds: -1}, "negative"},
	} {
		err := tc.s.Validate(tc.name)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
}

func TestServerWarningsAndSecrets(t *testing.T) {
	plain := Server{URL: "http://api.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}}
	if w := plain.Warnings("r"); len(w) != 1 || !strings.Contains(w[0], "plain http") {
		t.Fatalf("credentials over plain http should warn: %v", w)
	}
	for _, ok := range []Server{
		{URL: "http://localhost:8080/mcp", Headers: map[string]string{"Authorization": "x"}},
		{URL: "http://127.0.0.1:8080/mcp", Headers: map[string]string{"Authorization": "x"}},
		{URL: "https://api.example.com/mcp", Headers: map[string]string{"Authorization": "x"}},
		{URL: "http://api.example.com/mcp"},
	} {
		if w := ok.Warnings("r"); len(w) != 0 {
			t.Errorf("%+v should not warn: %v", ok, w)
		}
	}

	for server, want := range map[string]bool{
		`{"headers":{"Authorization":"Bearer abc"}}`:      true,
		`{"headers":{"Authorization":"Bearer ${TOKEN}"}}`: false,
		`{"env":{"GITHUB_TOKEN":"ghp_abc"}}`:              true,
		`{"env":{"GITHUB_TOKEN":"${GH}"}}`:                false,
		`{"env":{"LOG_LEVEL":"debug"}}`:                   false,
		`{"headers":{"X-Api-Key":""}}`:                    false,
	} {
		var s Server
		json.Unmarshal([]byte(server), &s)
		if s.HasInlineSecret() != want {
			t.Errorf("%s: HasInlineSecret = %v, want %v", server, !want, want)
		}
	}
}

func TestExpand(t *testing.T) {
	s := Server{
		URL: "https://${HOST}/mcp", Args: []string{"--root", "${ROOT}", "$NOT_EXPANDED"},
		Env: map[string]string{"K": "${KEY}"}, Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	}
	got, err := s.expand(envMap("HOST", "h.example", "ROOT", "/data", "KEY", "kv", "TOKEN", "tv"))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://h.example/mcp" || got.Args[1] != "/data" || got.Args[2] != "$NOT_EXPANDED" || got.Env["K"] != "kv" || got.Headers["Authorization"] != "Bearer tv" {
		t.Fatalf("%+v", got)
	}
	if s.Headers["Authorization"] != "Bearer ${TOKEN}" {
		t.Fatal("expand must not modify the original")
	}
	if _, err := s.expand(envMap("HOST", "h", "ROOT", "r", "KEY", "k")); err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("an unset variable must be named in the error: %v", err)
	}
}

func TestEnvironmentIsAllowlisted(t *testing.T) {
	getenv := envMap("PATH", "/bin", "HOME", "/home/u", "OPENAI_API_KEY", "sk-secret", "TALARIA_TELEGRAM_TOKEN", "bot-secret", "LANG", "C")
	env := Server{Env: map[string]string{"MY_VAR": "v"}}.environment(getenv)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "sk-secret") || strings.Contains(joined, "bot-secret") {
		t.Fatalf("secrets leaked: %v", env)
	}
	for _, want := range []string{"PATH=/bin", "HOME=/home/u", "LANG=C", "MY_VAR=v"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
}

func TestApprovalPolicy(t *testing.T) {
	cases := []struct {
		s    Server
		tool string
		want bool
	}{
		{Server{}, "read", false},
		{Server{Trusted: true}, "read", true},
		{Server{AutoApprove: []string{"read"}}, "read", true},
		{Server{AutoApprove: []string{"read"}}, "write", false},
	}
	for i, c := range cases {
		if got := c.s.approves(c.tool, ClassOther); got != c.want {
			t.Errorf("case %d: approves = %v, want %v", i, got, c.want)
		}
	}
}

func TestRenderResult(t *testing.T) {
	for name, tc := range map[string]struct {
		in      string
		want    string
		wantErr string
	}{
		"text":            {`{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`, "a\nb", ""},
		"complete":        {`{"resultType":"complete","content":[{"type":"text","text":"ok"}]}`, "ok", ""},
		"audio":           {`{"content":[{"type":"audio","mimeType":"audio/wav","data":"AAAA"}]}`, "[audio audio/wav, 3 bytes, not shown]", ""},
		"padded image":    {`{"content":[{"type":"image","mimeType":"image/png","data":"QUI="}]}`, "[image image/png, 2 bytes, not shown]", ""},
		"binary resource": {`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"app/bin","blob":"AA=="}}]}`, "[binary resource file:///x app/bin, not shown]", ""},
		"unknown block":   {`{"content":[{"type":"hologram"}]}`, `[unsupported content type "hologram"]`, ""},
		"structured only": {`{"structuredContent":{"a":[1, 2]}}`, `{"a":[1,2]}`, ""},
		"empty":           {`{}`, "", ""},
		"error with text": {`{"isError":true,"content":[{"type":"text","text":"bad date"}]}`, "", "bad date"},
		"error without":   {`{"isError":true}`, "", "without details"},
		"input required":  {`{"resultType":"input_required"}`, "", "interactive input"},
		"unknown type":    {`{"resultType":"mystery"}`, "", `"mystery"`},
		"garbage":         {`[1,2`, "", "unreadable"},
	} {
		got, err := renderResult(json.RawMessage(tc.in))
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want containing %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{max: 10}
	b.Write([]byte("0123456789"))
	b.Write([]byte("abc\n  def"))
	if got := b.String(); got != "9abc def" {
		t.Fatalf("got %q", got)
	}
	if len(b.buf) > 10 {
		t.Fatalf("buffer grew to %d", len(b.buf))
	}
}
