package files

import (
	"context"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func viewTool(t *testing.T, s *Store) tool.Tool {
	t.Helper()
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s})
	tl, ok := reg.Get("files_view")
	if !ok {
		t.Fatal("files_view not registered")
	}
	return tl
}

func TestViewShowsAnImageToTheModel(t *testing.T) {
	s := testStore(t)
	f, err := s.Save("shot.png", append(pngProbe, make([]byte, 100)...), OriginUpload)
	if err != nil {
		t.Fatal(err)
	}
	res, err := viewTool(t, s).Execute(context.Background(), []byte(`{"id":"`+f.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].MIME != "image/png" {
		t.Fatalf("%+v", res.Images)
	}
	if res.Text == "" {
		t.Fatal("the model needs a line of text with its picture")
	}
}

func TestViewRefusesPdfsAndMissingFiles(t *testing.T) {
	s := testStore(t)
	f, err := s.MakePDF("Doc", "hello")
	if err != nil {
		t.Fatal(err)
	}
	res, err := viewTool(t, s).Execute(context.Background(), []byte(`{"id":"`+f.ID+`"}`))
	if err != nil || len(res.Images) != 0 {
		t.Fatalf("a PDF is not a picture: %v %+v", err, res)
	}
	if _, err := viewTool(t, s).Execute(context.Background(), []byte(`{"id":"nope"}`)); err == nil {
		t.Fatal("a missing file was shown")
	}
}

func TestReadQuotesAPdf(t *testing.T) {
	s := testStore(t)
	f, err := s.MakePDF("Notes", "first line\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s})
	rd, ok := reg.Get("files_read")
	if !ok {
		t.Fatal("files_read not registered")
	}
	out, err := rd.Run(context.Background(), []byte(`{"id":"`+f.ID+`"}`))
	if err != nil || !strings.Contains(out, "first line") || !strings.Contains(out, "second line") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := rd.Run(context.Background(), []byte(`{"id":"nope"}`)); err == nil {
		t.Fatal("a missing file was read")
	}
}

func TestSendDeliversThroughTheSender(t *testing.T) {
	s := testStore(t)
	f, err := s.MakePDF("Doc", "hi")
	if err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotBytes []byte
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s, ChatID: 9,
		Send: func(_ context.Context, chatID int64, name string, data []byte) error {
			if chatID != 9 {
				t.Fatalf("the file goes to this chat: %d", chatID)
			}
			gotName, gotBytes = name, data
			return nil
		}})
	snd, ok := reg.Get("files_send")
	if !ok {
		t.Fatal("files_send not registered")
	}
	out, err := snd.Run(context.Background(), []byte(`{"id":"`+f.ID+`"}`))
	if err != nil || !strings.Contains(out, "sent") {
		t.Fatalf("%q %v", out, err)
	}
	if gotName != f.Name || string(gotBytes) == "" {
		t.Fatal("the sender did not get the file")
	}
	reg2 := tool.NewRegistry()
	RegisterTools(reg2, Binding{Store: s})
	plain, _ := reg2.Get("files_send")
	if _, err := plain.Run(context.Background(), []byte(`{"id":"`+f.ID+`"}`)); err == nil {
		t.Fatal("sending without a service should say so")
	}
}
