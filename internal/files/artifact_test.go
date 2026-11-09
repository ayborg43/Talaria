package files

import (
	"context"
	"encoding/json"
	"testing"

	"talaria/internal/tool"
)

func TestMakingAFileReturnsItAsARecord(t *testing.T) {
	s := testStore(t)
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s})
	mk, _ := reg.Get("files_make_pdf")
	res, err := mk.Execute(context.Background(), json.RawMessage(`{"title":"Notes","text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kind, ID, Name, MIME string
		Size                 int
	}
	if err := json.Unmarshal(res.Artifact, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Artifact)
	}
	if got.Kind != "document" || got.ID == "" || got.Name != "Notes.pdf" || got.MIME != "application/pdf" || got.Size == 0 {
		t.Fatalf("%+v", got)
	}
	if _, _, err := s.Open(got.ID); err != nil {
		t.Fatalf("the record names a file that is not kept: %v", err)
	}
}
