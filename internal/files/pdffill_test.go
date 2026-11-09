package files

import (
	"fmt"
	"strings"
	"testing"
)

// formPDF builds a small one-page form: a text field "full_name" and a
// checkbox "agree", with correct cross-reference offsets.
func formPDF(t *testing.T) []byte {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [4 0 R 5 0 R] >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Annots [4 0 R 5 0 R] /Resources << /Font << /Helv 6 0 R >> >> >>",
		"<< /Type /Annot /Subtype /Widget /Rect [50 700 300 722] /FT /Tx /T (full_name) /V () /DA (/Helv 12 Tf 0 g) /F 4 >>",
		"<< /Type /Annot /Subtype /Widget /Rect [50 650 70 672] /FT /Btn /T (agree) /V /Off /DA (/Helv 12 Tf 0 g) /F 4 /AS /Off /AP << /N << /Yes 7 0 R /Off 8 0 R >> >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 20 22] /Length 0 >>\nstream\n\nendstream",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 20 22] /Length 0 >>\nstream\n\nendstream",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func TestInspectAndFillAForm(t *testing.T) {
	raw := formPDF(t)
	fields, err := InspectForm(raw)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range fields {
		names = append(names, f.Name+":"+f.Type)
	}
	if !strings.Contains(strings.Join(names, ","), "full_name") || !strings.Contains(strings.Join(names, ","), "agree") {
		t.Fatalf("fields: %v", names)
	}
	filled, err := FillForm(raw, map[string]string{"full_name": "Ada Example", "agree": "true"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := InspectForm(filled)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range after {
		got[f.Name] = f.Value
	}
	if got["full_name"] != "Ada Example" {
		t.Fatalf("text field not filled: %v", got)
	}
	if got["agree"] != "true" && got["agree"] != "Yes" {
		t.Fatalf("checkbox not filled: %v", got)
	}
	if _, err := FillForm(raw, map[string]string{"nope": "x"}); err == nil {
		t.Fatal("an unknown field was accepted")
	}
}

func TestFormToolsRefuseWhatTheyCannotDo(t *testing.T) {
	if _, err := InspectForm([]byte("not a pdf")); err == nil {
		t.Fatal("a non-PDF was inspected")
	}
	if _, err := InspectForm(GeneratePDF("T", "hello")); err == nil || !strings.Contains(err.Error(), "no form fields") {
		t.Fatalf("a PDF with no form should say so: %v", err)
	}
	// An encrypted-looking file (an /Encrypt entry) is refused by name.
	enc := strings.Replace(string(formPDF(t)), "/Root 1 0 R", "/Root 1 0 R /Encrypt 9 0 R", 1)
	if _, err := InspectForm([]byte(enc)); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("encrypted not refused by name: %v", err)
	}
	truncated := formPDF(t)[:100]
	if _, err := InspectForm(truncated); err == nil {
		t.Fatal("a truncated PDF was inspected")
	}
}
