package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/oauth"
	"talaria/internal/oauth/oauthtest"
	"talaria/internal/tool"
	"talaria/internal/vault"
)

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name string
		ann  Annotations
		want Class
	}{
		// what reads
		{"list_events", Annotations{}, ClassRead},
		{"get_event", Annotations{}, ClassRead},
		{"search_mail", Annotations{}, ClassRead},
		{"search_email", Annotations{}, ClassRead},
		{"listEvents", Annotations{}, ClassRead},
		{"read_message", Annotations{}, ClassRead},
		{"list_drafts", Annotations{}, ClassRead},
		{"find-free-time", Annotations{}, ClassRead},
		{"calendar_lookup", Annotations{ReadOnly: yes()}, ClassRead},
		{"freebusy", Annotations{ReadOnly: yes()}, ClassRead},
		// what sends
		{"send_email", Annotations{}, ClassSend},
		{"send_draft", Annotations{}, ClassSend},
		{"reply_to_thread", Annotations{}, ClassSend},
		{"forward_message", Annotations{}, ClassSend},
		{"post_message", Annotations{}, ClassSend},
		{"share_document", Annotations{}, ClassSend},
		{"invite_attendees", Annotations{}, ClassSend},
		{"pay_invoice", Annotations{}, ClassSend},
		{"sendEmail", Annotations{}, ClassSend},
		{"email_user", Annotations{}, ClassSend},
		{"do_thing", Annotations{OpenWorld: yes()}, ClassSend}, // the server says it reaches outside
		// what deletes
		{"delete_event", Annotations{}, ClassDelete},
		{"trash_message", Annotations{}, ClassDelete},
		{"remove_label", Annotations{}, ClassDelete},
		{"cancel_meeting", Annotations{}, ClassDelete},
		{"delete_draft", Annotations{}, ClassDelete},
		{"purge", Annotations{}, ClassDelete},
		{"manage_things", Annotations{Destructive: yes()}, ClassDelete},
		// what drafts
		{"create_draft", Annotations{}, ClassDraft},
		{"update_draft", Annotations{}, ClassDraft},
		{"draft_reply", Annotations{}, ClassSend}, // a reply: the word reply wins, because it is the riskier reading
		{"save_draft", Annotations{}, ClassDraft},
		// what is not recognised asks
		{"create_event", Annotations{}, ClassOther},
		{"update_event", Annotations{}, ClassOther},
		{"mark_as_read", Annotations{}, ClassOther},
		{"archive_thread", Annotations{ReadOnly: yes()}, ClassOther}, // a hint that contradicts the name is not believed
		{"manage_calendar", Annotations{}, ClassOther},
		{"", Annotations{}, ClassOther},
		{"run", Annotations{}, ClassOther},
		// hints only make things more careful, or agree with the name
		{"get_secrets", Annotations{Destructive: yes()}, ClassDelete},
		{"list_things", Annotations{OpenWorld: yes()}, ClassOther}, // the name and the hint disagree: it asks
		{"search_everything", Annotations{OpenWorld: yes(), ReadOnly: yes()}, ClassRead},
		{"delete_thing", Annotations{ReadOnly: yes()}, ClassDelete}, // a lie about reading does not free a delete
		{"send_thing", Annotations{ReadOnly: yes()}, ClassSend},
	} {
		if got := Classify(tc.name, tc.ann); got != tc.want {
			t.Errorf("Classify(%q, %+v) = %s, want %s", tc.name, tc.ann, got, tc.want)
		}
	}
	for _, c := range []Class{ClassSend, ClassDelete} {
		if !c.Sensitive() {
			t.Errorf("%s is sensitive", c)
		}
	}
	for _, c := range []Class{ClassRead, ClassDraft, ClassOther} {
		if c.Sensitive() {
			t.Errorf("%s is not", c)
		}
	}
}

func TestPermissionLevels(t *testing.T) {
	for _, tc := range []struct {
		s     Server
		class Class
		want  bool // runs without asking
	}{
		{Server{}, ClassRead, false}, // the default is what it always was
		{Server{Permissions: PermAsk}, ClassRead, false},
		{Server{Permissions: PermReads}, ClassRead, true},
		{Server{Permissions: PermReads}, ClassDraft, false},
		{Server{Permissions: PermReads}, ClassOther, false},
		{Server{Permissions: PermReads}, ClassSend, false},
		{Server{Permissions: PermReads}, ClassDelete, false},
		{Server{Permissions: PermReadsAndDraft}, ClassRead, true},
		{Server{Permissions: PermReadsAndDraft}, ClassDraft, true},
		{Server{Permissions: PermReadsAndDraft}, ClassOther, false},
		{Server{Permissions: PermReadsAndDraft}, ClassSend, false},
		{Server{Permissions: PermReadsAndDraft}, ClassDelete, false},
		// What the user said about the server or a tool by name still holds.
		{Server{Trusted: true}, ClassSend, true},
		{Server{AutoApprove: []string{"y"}}, ClassSend, false}, // another tool's listing is not this one's
	} {
		if got := tc.s.approves("x", tc.class); got != tc.want {
			t.Errorf("%+v %s: %v, want %v", tc.s, tc.class, got, tc.want)
		}
	}
	if !(Server{AutoApprove: []string{"send_email"}}).approves("send_email", ClassSend) {
		t.Fatal("a tool the user listed by name is theirs to allow")
	}
}

func TestConnectorSettingsAreValidated(t *testing.T) {
	ok := Server{URL: "https://mail.example.com/mcp", OAuth: &OAuthConfig{Scopes: []string{"mail"}}, Permissions: PermReads}
	if err := ok.Validate("mail"); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Server{
		"oauth on a command":      {Command: "x", OAuth: &OAuthConfig{}},
		"oauth and headers":       {URL: "https://x.example/mcp", OAuth: &OAuthConfig{}, Headers: map[string]string{"Authorization": "Bearer x"}},
		"bad redirect port":       {URL: "https://x.example/mcp", OAuth: &OAuthConfig{RedirectPort: 70000}},
		"secret without a client": {URL: "https://x.example/mcp", OAuth: &OAuthConfig{ClientSecretEnv: "S"}},
		"unknown permissions":     {URL: "https://x.example/mcp", Permissions: "everything"},
	} {
		if err := s.Validate("x"); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func connectorTools() []oauthtest.Tool {
	read := map[string]any{"readOnlyHint": true}
	return []oauthtest.Tool{
		{Name: "list_events", Description: "calendar", Annotations: read, Run: func(map[string]any) string { return "09:00 standup; 14:00 review" }},
		{Name: "search_mail", Annotations: read, Run: func(map[string]any) string { return "1 message from Ada" }},
		{Name: "create_draft", Run: func(a map[string]any) string { return "draft saved: " + a["subject"].(string) }},
		{Name: "send_email", Annotations: map[string]any{"openWorldHint": true}, Run: func(map[string]any) string { return "sent" }},
		{Name: "delete_event", Annotations: map[string]any{"destructiveHint": true}, Run: func(map[string]any) string { return "deleted" }},
		{Name: "create_event", Run: func(map[string]any) string { return "created" }},
	}
}

// connected signs in to a fake server and starts the manager against it.
func connected(t *testing.T, srv *oauthtest.Server, spec Server) (*Manager, *vault.Vault) {
	t.Helper()
	vault.ForTests(t)
	v, err := vault.Create(filepath.Join(t.TempDir(), "v.enc"), "a long enough passphrase")
	if err != nil {
		t.Fatal(err)
	}
	data, err := oauth.Login{ServerURL: srv.MCPURL(), Open: openAndFollow, Timeout: 10 * time.Second}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := oauth.Save(v, "mail", data); err != nil {
		t.Fatal(err)
	}
	spec.URL, spec.OAuth = srv.MCPURL(), &OAuthConfig{}
	m := StartAuth(context.Background(), map[string]Server{"mail": spec}, func(string) string { return "" },
		map[string]TokenSource{"mail": &oauth.Source{Vault: v, Server: "mail"}})
	t.Cleanup(m.Close)
	return m, v
}

func registry(m *Manager) *tool.Registry {
	r := tool.NewRegistry()
	m.Register(r)
	return r
}

func callTool(t *testing.T, r *tool.Registry, name, args string) (string, error) {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl.Run(context.Background(), json.RawMessage(args))
}

func TestAConnectorWithReadsAndDraftsFreeAsksAboutTheRest(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	m, _ := connected(t, srv, Server{Permissions: PermReadsAndDraft})

	if st := m.Status(); len(st) != 1 || st[0].Err != nil || len(st[0].Tools) != 6 {
		t.Fatalf("%+v", st)
	}
	want := map[string]struct {
		class Class
		asks  bool
	}{
		"mcp_mail_list_events": {ClassRead, false}, "mcp_mail_search_mail": {ClassRead, false},
		"mcp_mail_create_draft": {ClassDraft, false}, "mcp_mail_send_email": {ClassSend, true},
		"mcp_mail_delete_event": {ClassDelete, true}, "mcp_mail_create_event": {ClassOther, true},
	}
	r := registry(m)
	for name, w := range want {
		tl, ok := r.Get(name)
		if !ok || tl.NeedsApproval != w.asks {
			t.Errorf("%s: registered=%v asks=%v, want asks=%v", name, ok, tl.NeedsApproval, w.asks)
		}
	}
	for _, info := range m.Status()[0].Tools {
		if w := want[info.Exposed]; info.Class != w.class || info.NeedsApproval != w.asks {
			t.Errorf("status of %s: %+v", info.Exposed, info)
		}
	}
	sens := m.Sensitive()
	if strings.Join(sortedCopy(sens), ",") != "mcp_mail_delete_event,mcp_mail_send_email" {
		t.Fatalf("only the tools that send or delete are never remembered: %v", sens)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestTheDefaultLevelStillAsksAboutEverything(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	m, _ := connected(t, srv, Server{})
	for _, info := range m.Status()[0].Tools {
		if !info.NeedsApproval {
			t.Errorf("%s runs without asking by default", info.Exposed)
		}
	}
	// Reads only.
	m2, _ := connected(t, srv, Server{Permissions: PermReads})
	free := 0
	for _, info := range m2.Status()[0].Tools {
		if !info.NeedsApproval {
			free++
			if info.Class != ClassRead {
				t.Errorf("%s is %s and runs without asking at the reads level", info.Exposed, info.Class)
			}
		}
	}
	if free != 2 {
		t.Fatalf("%d free", free)
	}
}

func TestCallsCarryTheStoredToken(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	m, v := connected(t, srv, Server{Permissions: PermReads})
	out, err := callTool(t, registry(m), "mcp_mail_list_events", `{}`)
	if err != nil || out != "09:00 standup; 14:00 review" {
		t.Fatalf("%q %v", out, err)
	}
	stored, _ := v.Get("oauth.mail")
	calls := srv.Served()
	if len(calls) != 1 || calls[0].Auth != stored.OAuth.AccessToken {
		t.Fatalf("%+v", calls)
	}
}

// The server stops accepting the token mid-session (it expired early, or was
// rotated): the client renews it and the call goes through, without anyone noticing.
func TestARefusedTokenIsRenewedAndTheCallRetried(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	srv.RotateRefresh = true
	m, v := connected(t, srv, Server{Permissions: PermReads})
	before, _ := v.Get("oauth.mail")
	srv.Expire()
	out, err := callTool(t, registry(m), "mcp_mail_list_events", `{}`)
	if err != nil || out == "" {
		t.Fatalf("%q %v", out, err)
	}
	after, _ := v.Get("oauth.mail")
	if srv.Refreshes != 1 || after.OAuth.AccessToken == before.OAuth.AccessToken || after.OAuth.RefreshToken == before.OAuth.RefreshToken {
		t.Fatalf("refreshes=%d: the new tokens should be saved", srv.Refreshes)
	}
	if calls := srv.Served(); calls[len(calls)-1].Auth != after.OAuth.AccessToken {
		t.Fatal("the retried call used the renewed token")
	}
	// And it keeps working.
	if _, err := callTool(t, registry(m), "mcp_mail_search_mail", `{}`); err != nil || srv.Refreshes != 1 {
		t.Fatalf("%v %d", err, srv.Refreshes)
	}
}

func TestASignInThatCannotBeRenewedSaysToSignInAgain(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	m, _ := connected(t, srv, Server{Permissions: PermReads})
	srv.RevokeEverything()
	_, err := callTool(t, registry(m), "mcp_mail_list_events", `{}`)
	if err == nil || !strings.Contains(err.Error(), "tal connect mail") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "at-") || strings.Contains(err.Error(), "rt-") {
		t.Fatalf("a token in an error: %v", err)
	}
}

func TestAServerThatNeedsASignInIsReportedNotFatal(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	vault.ForTests(t)
	v, _ := vault.Create(filepath.Join(t.TempDir(), "v.enc"), "a long enough passphrase") // no sign-in saved
	spec := Server{URL: srv.MCPURL(), OAuth: &OAuthConfig{}}
	m := StartAuth(context.Background(), map[string]Server{"mail": spec, "other": {URL: srv.MCPURL(), OAuth: &OAuthConfig{}}}, func(string) string { return "" },
		map[string]TokenSource{"mail": &oauth.Source{Vault: v, Server: "mail"}, "other": &oauth.Source{Vault: v, Server: "other"}})
	defer m.Close()
	for _, st := range m.Status() {
		if st.Err == nil || !strings.Contains(st.Err.Error(), "tal connect "+st.Name) {
			t.Errorf("%s: %v", st.Name, st.Err)
		}
	}
	// With no vault at all, it says how to make one.
	m2 := StartAuth(context.Background(), map[string]Server{"mail": spec}, func(string) string { return "" }, nil)
	defer m2.Close()
	if err := m2.Status()[0].Err; err == nil || !strings.Contains(err.Error(), "tal vault init") {
		t.Fatalf("%v", err)
	}
	if len(srv.Served()) != 0 {
		t.Fatal("nothing is sent to a server we are not signed in to")
	}
}

func TestA401WithoutOAuthSuggestsIt(t *testing.T) {
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	m := Start(context.Background(), map[string]Server{"mail": {URL: srv.MCPURL()}}, func(string) string { return "" })
	defer m.Close()
	err := m.Status()[0].Err
	if err == nil || !strings.Contains(err.Error(), `"oauth": {}`) || strings.Contains(err.Error(), "not supported") {
		t.Fatalf("%v", err)
	}
}

func TestNoTokenIsSentToAServerWithoutOAuth(t *testing.T) {
	// A stored sign-in is for one server: a different one, configured without oauth, gets nothing of it.
	srv := oauthtest.New()
	defer srv.Close()
	srv.Tools = connectorTools()
	connected(t, srv, Server{})
	other := oauthtest.New()
	defer other.Close()
	other.Tools = connectorTools()
	Start(context.Background(), map[string]Server{"x": {URL: other.MCPURL()}}, func(string) string { return "" }).Close()
	if len(other.Served()) != 0 {
		t.Fatal("served without a token")
	}
	for _, l := range other.Log {
		if strings.Contains(l, "token") {
			t.Fatal("talked to an authorization server it was not asked to use")
		}
	}
}
