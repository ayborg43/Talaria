package config

import (
	"strings"
	"testing"
)

func TestWithNoFallbacksNamedEveryOtherProfileIsTheFallback(t *testing.T) {
	c := &Config{DefaultProfile: "main", Profiles: map[string]Profile{"a": {}, "main": {}, "z": {}, "b": {}}}
	if got := strings.Join(c.AutoFallbacks("a"), ","); got != "main,b,z" {
		t.Errorf("the default first, then by name, never the profile itself: %s", got)
	}
	if got := strings.Join(c.AutoFallbacks("main"), ","); got != "a,b,z" {
		t.Errorf("from the default: %s", got)
	}
	c.NoAutoFallback = true
	if got := c.AutoFallbacks("a"); len(got) != 0 {
		t.Errorf("switched off: %v", got)
	}
}
