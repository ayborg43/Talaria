package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// paramHeader says an argument must be copied into an Mcp-Param-<Name> header.
type paramHeader struct {
	Name string   // the part after Mcp-Param-
	Path []string // property path to the argument
}

const tchars = "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// mirroredParams reads the x-mcp-header annotations of a tool's input schema,
// which the Streamable HTTP binding of the stateless revision requires clients
// to mirror into HTTP headers. It rejects a schema that uses them wrongly, in
// which case the tool must not be offered.
func mirroredParams(schema json.RawMessage) ([]paramHeader, error) {
	var root map[string]any
	if json.Unmarshal(schema, &root) != nil {
		return nil, nil
	}
	total := countKey(root, "x-mcp-header")
	if total == 0 {
		return nil, nil
	}

	var out []paramHeader
	seen := map[string]bool{}
	var walk func(node map[string]any, path []string) error
	walk = func(node map[string]any, path []string) error {
		props, _ := node["properties"].(map[string]any)
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)

		for _, n := range names {
			sub, _ := props[n].(map[string]any)
			if sub == nil {
				continue
			}
			here := append(append([]string{}, path...), n)
			if raw, annotated := sub["x-mcp-header"]; annotated {
				name, _ := raw.(string)
				if err := checkHeaderName(name); err != nil {
					return err
				}
				if seen[strings.ToLower(name)] {
					return fmt.Errorf("x-mcp-header %q is used twice", name)
				}
				seen[strings.ToLower(name)] = true
				if typ, _ := sub["type"].(string); typ != "string" && typ != "integer" && typ != "boolean" {
					return fmt.Errorf("x-mcp-header %q is on a parameter that is not a string, integer or boolean", name)
				}
				out = append(out, paramHeader{Name: name, Path: here})
			}
			if typ, _ := sub["type"].(string); typ == "object" {
				if err := walk(sub, here); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, nil); err != nil {
		return nil, err
	}
	if len(out) != total {
		return nil, errors.New("x-mcp-header appears where it cannot be mirrored (only plain properties qualify)")
	}
	return out, nil
}

func checkHeaderName(name string) error {
	if name == "" {
		return errors.New("x-mcp-header is empty")
	}
	for _, r := range name {
		if r > 127 || !strings.ContainsRune(tchars, r) {
			return fmt.Errorf("x-mcp-header %q is not a valid HTTP header name", name)
		}
	}
	return nil
}

func countKey(v any, key string) int {
	n := 0
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if k == key {
				n++
			}
			n += countKey(sub, key)
		}
	case []any:
		for _, sub := range t {
			n += countKey(sub, key)
		}
	}
	return n
}

// mirroredHeaders computes the Mcp-Param-* headers for a call. A missing or
// null argument adds no header.
func mirroredHeaders(params []paramHeader, args map[string]any) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, p := range params {
		var cur any = args
		for _, step := range p.Path {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[step]
		}

		var s string
		switch v := cur.(type) {
		case string:
			s = v
		case bool:
			s = strconv.FormatBool(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				s = strconv.FormatInt(n, 10)
			} else if f, err := v.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) <= 1<<53-1 {
				s = strconv.FormatInt(int64(f), 10)
			} else {
				continue
			}
		default:
			continue
		}
		out["Mcp-Param-"+p.Name] = encodeHeaderValue(s)
	}
	return out
}

// encodeHeaderValue returns s as a header value. Anything that is not plain
// visible ASCII, has edge whitespace, or looks like the encoding itself is
// sent as =?base64?<utf-8 in base64>?=.
func encodeHeaderValue(s string) string {
	plain := s == strings.Trim(s, " \t") && !(strings.HasPrefix(s, "=?base64?") && strings.HasSuffix(s, "?="))
	for i := 0; plain && i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			plain = false
		}
	}
	if plain {
		return s
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}
