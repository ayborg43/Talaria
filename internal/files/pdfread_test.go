package files

import (
	"bytes"
	"compress/flate"
	"strings"
	"testing"
)

func TestExtractRoundTripsGeneratedPDF(t *testing.T) {
	raw := GeneratePDF("Shopping (list)", "eggs\nmilk (2l)\ncaf\xe9")
	text, err := ExtractText(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Shopping (list)", "eggs", "milk (2l)", "caf"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestExtractSaysWhenThereIsNothing(t *testing.T) {
	if _, err := ExtractText([]byte("not a pdf at all")); err == nil {
		t.Fatal("garbage was read as a PDF")
	}
	// A PDF with no text-showing operators is not an error, just empty.
	empty := []byte("%PDF-1.4\n1 0 obj\n<< /Length 11 >>\nstream\nBT /F1 12 Tf ET\nendstream\nendobj\n")
	text, err := ExtractText(empty)
	if err != nil || strings.TrimSpace(text) != "" {
		t.Fatalf("%q %v", text, err)
	}
}

func TestExtractReadsFlateStreams(t *testing.T) {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	w.Write([]byte("BT (hidden words) Tj ET"))
	w.Close()
	raw := append([]byte("%PDF-1.4\n1 0 obj\n<< /Length 0 /Filter /FlateDecode >>\nstream\n"), buf.Bytes()...)
	raw = append(raw, []byte("\nendstream\nendobj\n")...)
	text, err := ExtractText(raw)
	if err != nil || !strings.Contains(text, "hidden words") {
		t.Fatalf("%q %v", text, err)
	}
}
