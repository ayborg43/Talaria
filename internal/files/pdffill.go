package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// FormField is one fillable field of a PDF form.
type FormField struct {
	Name  string
	Type  string // text, checkbox, date, radio, choice, list or other
	Value string
}

func formConfig() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.Optimize = false
	return conf
}

// checkForm refuses what the form tools will not touch, by name.
func checkForm(raw []byte) error {
	if len(raw) < 5 || string(raw[:5]) != "%PDF-" {
		return fmt.Errorf("not a PDF")
	}
	if len(raw) > MaxBytes {
		return fmt.Errorf("the PDF is too big (%d MB at most)", MaxBytes>>10)
	}
	head := raw
	if len(head) > 1<<20 {
		head = head[:1<<20]
	}
	if bytes.Contains(head, []byte("/Encrypt")) {
		return fmt.Errorf("the PDF is encrypted, which form filling does not open")
	}
	if bytes.Contains(head, []byte("/XFA")) {
		return fmt.Errorf("the PDF uses XFA forms, which are not supported")
	}
	return nil
}

// InspectForm lists a PDF's fillable fields.
func InspectForm(raw []byte) ([]FormField, error) {
	if err := checkForm(raw); err != nil {
		return nil, err
	}
	fields, err := api.FormFields(context.Background(), bytes.NewReader(raw), formConfig())
	if err != nil {
		if strings.Contains(err.Error(), "no form available") {
			return nil, fmt.Errorf("this PDF has no form fields to fill")
		}
		return nil, fmt.Errorf("the form could not be read: %v", scrubPDFErr(err))
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("this PDF has no form fields to fill")
	}
	out := make([]FormField, 0, len(fields))
	for _, f := range fields {
		typ := strings.ToLower(f.Typ.String())
		out = append(out, FormField{Name: f.Name, Type: typ, Value: f.V})
	}
	return out, nil
}

// scrubPDFErr trims pdfcpu's long errors to their first line.
func scrubPDFErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// FillForm fills fields by name: text and date fields take their value,
// checkboxes take true/false (also yes/on). An unknown name is refused, so
// a mistyped field cannot silently produce an unfilled copy.
func FillForm(raw []byte, values map[string]string) ([]byte, error) {
	if err := checkForm(raw); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("give at least one field to fill")
	}
	ctx := context.Background()
	group, err := api.ExportForm(ctx, bytes.NewReader(raw), "", formConfig())
	if err != nil {
		return nil, fmt.Errorf("the form could not be read: %v", scrubPDFErr(err))
	}
	seen := map[string]bool{}
	set := func(name string) (string, bool) {
		v, ok := values[name]
		if ok {
			seen[name] = true
		}
		return v, ok
	}
	for _, f := range group.Forms {
		for _, tf := range f.TextFields {
			if v, ok := set(tf.Name); ok {
				tf.Value = v
			}
		}
		for _, df := range f.DateFields {
			if v, ok := set(df.Name); ok {
				df.Value = v
			}
		}
		for _, cb := range f.CheckBoxes {
			if v, ok := set(cb.Name); ok {
				cb.Value = v == "true" || v == "yes" || v == "on" || v == "Yes"
			}
		}
		for _, cb := range f.ComboBoxes {
			if v, ok := set(cb.Name); ok {
				cb.Value = v
			}
		}
		for _, lb := range f.ListBoxes {
			if v, ok := set(lb.Name); ok {
				lb.Values = []string{v}
			}
		}
	}
	for name := range values {
		if !seen[name] {
			return nil, fmt.Errorf("the form has no field called %q", name)
		}
	}
	encoded, err := json.Marshal(group)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := api.FillForm(ctx, bytes.NewReader(raw), bytes.NewReader(encoded), &buf, formConfig()); err != nil {
		return nil, fmt.Errorf("the form could not be filled: %v", scrubPDFErr(err))
	}
	return buf.Bytes(), nil
}
