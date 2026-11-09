package mailbox

import (
	"context"
	"encoding/json"
	"testing"

	"talaria/internal/tool"
)

func TestMailToolsReturnRecordsBesideTheirText(t *testing.T) {
	_, s := seeded(t)
	r := tool.NewRegistry()
	RegisterTools(r, s)
	list, _ := r.Get("email_list")
	res, err := list.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kind  string
		Items []struct {
			ID                  int
			From, Subject, When string
			Unread              bool
		}
	}
	if err := json.Unmarshal(res.Artifact, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Artifact)
	}
	if got.Kind != "mail_list" || len(got.Items) < 2 || got.Items[0].ID != 42 || !got.Items[0].Unread || got.Items[0].From == "" || got.Items[0].When == "" {
		t.Fatalf("%+v", got)
	}
	text, _ := s.List(context.Background(), Query{})
	if res.Text != text {
		t.Fatal("the text the model reads changed")
	}

	read, _ := r.Get("email_read")
	res, err = read.Execute(context.Background(), json.RawMessage(`{"id":42}`))
	if err != nil {
		t.Fatal(err)
	}
	var one struct {
		Kind, From, Subject, Excerpt string
		ID                           int
	}
	if err := json.Unmarshal(res.Artifact, &one); err != nil || one.Kind != "mail" || one.ID != 42 || one.From == "" || one.Subject == "" {
		t.Fatalf("%v %+v", err, one)
	}
}
