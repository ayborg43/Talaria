package guard

import (
	"strings"
	"testing"
)

// A Meta Model API key is recognised by its shape, whether or not Talaria was told it.
func TestAMetaModelAPIKeyIsScrubbedByItsShape(t *testing.T) {
	r := NewRedactor(nil)
	out := r.Scrub("export MODEL_API_KEY=LLM_1607358788850350_nx9AbCdEfGhIjKlMnOpQrStUvWxYz012345LJY and LLM_models are fine")
	if strings.Contains(out, "nx9AbCdEf") || !strings.Contains(out, "[redacted]") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(out, "LLM_models are fine") {
		t.Fatalf("ordinary text was taken for a key: %q", out)
	}
}
