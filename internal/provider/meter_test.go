package provider

import (
	"context"
	"strings"
	"testing"
)

func TestOpenAIStreamUsage(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hi"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":7}}

data: [DONE]

`
	msg, err := readOpenAIStream(strings.NewReader(body), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Usage.In != 120 || msg.Usage.Out != 7 {
		t.Fatalf("usage %+v", msg.Usage)
	}
}

func TestOpenAIPlainUsage(t *testing.T) {
	msg, err := parseOpenAI([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	if err != nil || msg.Usage.In != 5 || msg.Usage.Out != 2 {
		t.Fatalf("%v %+v", err, msg.Usage)
	}
}

func TestAnthropicUsage(t *testing.T) {
	msg, err := parseAnthropic([]byte(`{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":4}}`))
	if err != nil || msg.Usage.In != 100 || msg.Usage.Out != 4 {
		t.Fatalf("%v %+v", err, msg.Usage)
	}
	body := `data: {"type":"message_start","message":{"usage":{"input_tokens":30,"output_tokens":1}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yo"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}

data: {"type":"message_stop"}

`
	msg, err = readAnthropicStream(strings.NewReader(body), func(string) {})
	if err != nil || msg.Usage.In != 30 || msg.Usage.Out != 9 {
		t.Fatalf("%v %+v", err, msg.Usage)
	}
}

type fakeP struct{ msg Message }

func (f fakeP) Chat(context.Context, Request) (Message, error) { return f.msg, nil }

func TestMeterReportsLabelAndEstimates(t *testing.T) {
	var got []Spent
	SetSink(func(s Spent) { got = append(got, s) })
	defer SetSink(nil)
	ctx := WithLabel(context.Background(), Label{Source: "job", Agent: "researcher"})
	ctx = WithLabel(ctx, Label{Kind: "code"}) // keeps what the outer part said
	p := Meter(fakeP{Message{Role: RoleAssistant, Content: strings.Repeat("a", 40), Usage: Usage{In: 11, Out: 3}}}, "strong", "m-1")
	p.Chat(ctx, Request{})
	q := Meter(fakeP{Message{Role: RoleAssistant, Content: strings.Repeat("a", 40)}}, "cheap", "m-2")
	q.Chat(context.Background(), Request{Messages: []Message{{Content: strings.Repeat("b", 80)}}})
	if len(got) != 2 {
		t.Fatalf("%d reports", len(got))
	}
	if g := got[0]; g.Profile != "strong" || g.Model != "m-1" || g.Usage.In != 11 || g.Usage.Estimated ||
		g.Label != (Label{Source: "job", Agent: "researcher", Kind: "code"}) {
		t.Fatalf("%+v", g)
	}
	if g := got[1]; !g.Usage.Estimated || g.Usage.Out != 10 || g.Usage.In != 22 {
		t.Fatalf("%+v", g)
	}
}
