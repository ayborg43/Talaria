package phone

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/tool"
)

// fakePhone is the phone's end: it answers each request through the link.
type fakePhone struct {
	mu     sync.Mutex
	link   *Link
	got    []string
	answer func(op string, args json.RawMessage) (any, string)
	closed bool
}

func (f *fakePhone) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *fakePhone) Send(b []byte) error {
	var m struct {
		ID   uint64          `json:"id"`
		Op   string          `json:"op"`
		Args json.RawMessage `json:"args"`
	}
	json.Unmarshal(b, &m)
	f.mu.Lock()
	f.got = append(f.got, m.Op+" "+string(m.Args))
	f.mu.Unlock()
	go func() {
		if f.answer == nil {
			return // the phone never answers
		}
		data, errText := f.answer(m.Op, m.Args)
		res := map[string]any{"t": "res", "id": m.ID, "ok": errText == "", "error": errText}
		if errText == "" {
			res["data"] = data
		}
		raw, _ := json.Marshal(res)
		f.link.Receive(raw)
	}()
	return nil
}

func linked(t *testing.T, h *Hub, f *fakePhone, caps ...string) {
	t.Helper()
	f.link = h.Attach(f)
	raw, _ := json.Marshal(map[string]any{"t": "hello", "name": "Test phone", "caps": caps})
	f.link.Receive(raw)
}

func fast() *Hub {
	return &Hub{ConnectWait: 50 * time.Millisecond, CallTimeout: 300 * time.Millisecond}
}

func TestACallReachesThePhoneAndComesBack(t *testing.T) {
	h := fast()
	f := &fakePhone{answer: func(op string, _ json.RawMessage) (any, string) { return map[string]any{"battery": 71}, "" }}
	linked(t, h, f, OpStatus)
	out, err := h.Call(context.Background(), OpStatus, nil)
	if err != nil || !strings.Contains(string(out), `"battery":71`) {
		t.Fatalf("got %s, %v", out, err)
	}
	if st := h.Status(); !st.Connected || st.Name != "Test phone" || len(st.Caps) != 1 {
		t.Errorf("status: %+v", st)
	}
}

func TestWithNoPhoneACallSaysSoAfterAShortWait(t *testing.T) {
	h := fast()
	began := time.Now()
	if _, err := h.Call(context.Background(), OpStatus, nil); err != ErrNotConnected {
		t.Fatalf("got %v", err)
	}
	if time.Since(began) < 40*time.Millisecond {
		t.Error("it did not wait for the phone to come back")
	}
	if (&Hub{}).Status().Connected {
		t.Error("a zero hub claims a phone")
	}
}

func TestAPhoneThatComesBackDuringTheWaitIsUsed(t *testing.T) {
	h := &Hub{ConnectWait: time.Second, CallTimeout: time.Second}
	h.joined = make(chan struct{})
	f := &fakePhone{answer: func(string, json.RawMessage) (any, string) { return "ok", "" }}
	go func() { time.Sleep(40 * time.Millisecond); linked(t, h, f, OpStatus) }()
	if _, err := h.Call(context.Background(), OpStatus, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWhatThePhoneHasNotAllowedIsNeverSent(t *testing.T) {
	h := fast()
	f := &fakePhone{answer: func(string, json.RawMessage) (any, string) { return "x", "" }}
	linked(t, h, f, OpStatus)
	_, err := h.Call(context.Background(), OpLocation, nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("got %v", err)
	}
	if len(f.got) != 0 {
		t.Errorf("the request went out anyway: %v", f.got)
	}
}

func TestThePhoneCanRefuseAndCanStayQuiet(t *testing.T) {
	h := fast()
	f := &fakePhone{answer: func(string, json.RawMessage) (any, string) { return nil, "Location permission was taken away." }}
	linked(t, h, f, OpLocation)
	if _, err := h.Call(context.Background(), OpLocation, nil); err == nil || !strings.Contains(err.Error(), "taken away") {
		t.Errorf("a refusal was lost: %v", err)
	}
	h2 := fast()
	quiet := &fakePhone{}
	linked(t, h2, quiet, OpStatus)
	if _, err := h2.Call(context.Background(), OpStatus, nil); err == nil || !strings.Contains(err.Error(), "in time") {
		t.Errorf("a silent phone: %v", err)
	}
}

func TestANewerConnectionReplacesTheOlderAndFailsItsCalls(t *testing.T) {
	h := &Hub{ConnectWait: 50 * time.Millisecond, CallTimeout: 2 * time.Second}
	old := &fakePhone{} // never answers
	linked(t, h, old, OpStatus)
	errs := make(chan error, 1)
	go func() { _, err := h.Call(context.Background(), OpStatus, nil); errs <- err }()
	time.Sleep(30 * time.Millisecond)
	fresh := &fakePhone{answer: func(string, json.RawMessage) (any, string) { return "fresh", "" }}
	linked(t, h, fresh, OpStatus)
	if err := <-errs; err != ErrNotConnected {
		t.Errorf("the old call ended with %v", err)
	}
	if !old.closed {
		t.Error("the older connection was left open")
	}
	if out, err := h.Call(context.Background(), OpStatus, nil); err != nil || !strings.Contains(string(out), "fresh") {
		t.Errorf("the newer phone was not used: %s %v", out, err)
	}
	old.link.Detach() // the old one finishing must not unlink the new one
	if !h.Status().Connected {
		t.Error("detaching the old connection unlinked the new one")
	}
}

func TestAnUnknownOperationFromAPhoneIsIgnored(t *testing.T) {
	h := fast()
	f := &fakePhone{}
	linked(t, h, f, OpStatus, "shell", OpStatus)
	if caps := h.Status().Caps; len(caps) != 1 || caps[0] != OpStatus {
		t.Errorf("caps: %v", caps)
	}
}

func TestTheToolsPassArgumentsAndCutLongAnswers(t *testing.T) {
	h := fast()
	var seen string
	f := &fakePhone{answer: func(op string, args json.RawMessage) (any, string) {
		seen = op + " " + string(args)
		return strings.Repeat("n", 20000), ""
	}}
	linked(t, h, f, OpCalendar, OpContacts, OpStatus)
	reg := tool.NewRegistry()
	RegisterTools(reg, h)
	rd, _ := reg.Get("phone_read")
	if !rd.Untrusted {
		t.Error("what other people wrote must count as outside content")
	}
	if in, _ := reg.Get("phone_info"); in.Untrusted {
		t.Error("the phone's own state is not outside content")
	}
	out, err := rd.Run(context.Background(), json.RawMessage(`{"what":"calendar","days":99}`))
	if err != nil || !strings.Contains(seen, `"days":14`) {
		t.Fatalf("days was not bounded: %q %v", seen, err)
	}
	if len([]rune(out)) > maxResult+200 || !strings.Contains(out, "cut") {
		t.Errorf("a long answer was not cut: %d", len(out))
	}
	if _, err := rd.Run(context.Background(), json.RawMessage(`{"what":"contacts"}`)); err == nil {
		t.Error("a contact lookup with no name was sent")
	}
	if _, err := rd.Run(context.Background(), json.RawMessage(`{"what":"sms"}`)); err == nil {
		t.Error("an unknown kind was sent")
	}
	in, _ := reg.Get("phone_info")
	if _, err := in.Run(context.Background(), json.RawMessage(`{"what":"contacts"}`)); err == nil {
		t.Error("phone_info accepted a contacts request")
	}
}
