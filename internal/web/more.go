package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"talaria/internal/browser"
	"talaria/internal/execenv"
	"talaria/internal/files"
	"talaria/internal/memory"
)

// This file is the app's hands-on side: the user's own profile and notes, the
// notification list, running a finished task again, reading and filling a PDF,
// and working the agent's computer and browser directly. None of it keeps
// anything running or in memory between requests: each handler reads what it
// needs, answers, and lets go.

// TaskInfo is a background task that has ended, for the Agents screen.
type TaskInfo struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Failed bool      `json:"failed"`
	At     time.Time `json:"at"`
	// Reviewed is set once the user has looked at the result and checked it off.
	Reviewed bool `json:"reviewed,omitempty"`
}

// ---- profile and notes ----

type notesView struct {
	Entries []string `json:"entries"`
	Used    int      `json:"used"`
	Limit   int      `json:"limit"`
}

func (s *Server) notesFor(t memory.Target) notesView {
	entries, used, limit, _ := s.Notes.Entries(t)
	if entries == nil {
		entries = []string{}
	}
	return notesView{Entries: entries, Used: used, Limit: limit}
}

func (s *Server) profileGet(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil {
		fail(w, http.StatusNotImplemented, "the profile is not set up here")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": s.Notes.Profile(), "user": s.notesFor(memory.User), "memory": s.notesFor(memory.Agent)})
}

// profilePost saves the profile, or adds, changes or removes one note. A note
// is named by where it was in the list and what it said, so an edit can never
// land on a different note than the one the user was looking at.
func (s *Server) profilePost(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil {
		fail(w, http.StatusNotImplemented, "the profile is not set up here")
		return
	}
	var in struct {
		Profile *memory.Profile `json:"profile"`
		// Onboarded marks the first-day introduction as done (or declined) without touching the rest of the profile.
		Onboarded *bool `json:"onboarded"`
		Note      *struct {
			Target string `json:"target"` // user or memory
			Index  *int   `json:"index"`  // nil adds a note
			Old    string `json:"old"`
			Text   string `json:"text"` // empty removes it
		} `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	var err error
	switch {
	case in.Onboarded != nil:
		p := s.Notes.Profile()
		p.Onboarded = *in.Onboarded
		err = s.Notes.SetProfile(p)
	case in.Profile != nil:
		p := *in.Profile
		p.Onboarded = s.Notes.Profile().Onboarded // the page's forms do not carry it, and must not undo it
		err = s.Notes.SetProfile(p)
	case in.Note != nil && in.Note.Index == nil:
		err = s.Notes.Add(memory.Target(in.Note.Target), in.Note.Text)
	case in.Note != nil:
		err = s.Notes.EditAt(memory.Target(in.Note.Target), *in.Note.Index, in.Note.Old, in.Note.Text)
	default:
		err = errors.New("say what to change: profile or note")
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.profileGet(w, r)
}

// ---- notifications kept in the app ----

func (s *Server) noticesGet(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Notice{}, "prefs": NoticePrefs{Approvals: true, Replies: true}})
		return
	}
	items, prefs := s.Notices.List()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "prefs": prefs})
}

func (s *Server) noticesPost(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		fail(w, http.StatusNotImplemented, "notifications are not kept here")
		return
	}
	var in struct {
		Clear bool         `json:"clear"`
		Prefs *NoticePrefs `json:"prefs"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Clear {
		s.Notices.Clear()
	}
	if in.Prefs != nil {
		s.Notices.SetPrefs(*in.Prefs)
	}
	s.noticesGet(w, r)
}

// ---- running a finished task again ----

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Retry  string `json:"retry"`
		Review string `json:"review"` // check a finished task off: it moves to Resolved
	}
	if !readJSON(w, r, &in) {
		return
	}
	switch {
	case strings.TrimSpace(in.Review) != "":
		if len(in.Review) > 64 {
			fail(w, http.StatusBadRequest, "that is not a task id")
			return
		}
		if s.Sources.ReviewTask == nil {
			fail(w, http.StatusNotImplemented, "tasks are not set up")
			return
		}
		if err := s.Sources.ReviewTask(strings.TrimSpace(in.Review)); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	case s.Sources.RetryTask == nil:
		fail(w, http.StatusNotImplemented, "tasks are not set up")
		return
	case strings.TrimSpace(in.Retry) == "":
		fail(w, http.StatusBadRequest, "say which task to run again or check off")
		return
	default:
		if err := s.Sources.RetryTask(strings.TrimSpace(in.Retry)); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out := map[string]any{"ok": true, "tasks": []TaskInfo{}}
	if s.Sources.Tasks != nil {
		if t := s.Sources.Tasks(); t != nil {
			out["tasks"] = t
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- PDF forms ----

// formGet lists a kept PDF's fillable fields, for the viewer's form panel.
func (s *Server) formGet(w http.ResponseWriter, r *http.Request) {
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	_, raw, err := st.Open(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "no such file: it may have expired")
		return
	}
	fields, err := files.InspectForm(raw)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"fields": []files.FormField{}, "note": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

// formFill writes the user's values into a copy of the form. The original is
// untouched, exactly as when the agent fills one.
func (s *Server) formFill(w http.ResponseWriter, r *http.Request) {
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	var in struct {
		Values map[string]string `json:"values"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Values) == 0 {
		fail(w, http.StatusBadRequest, "nothing was filled in")
		return
	}
	f, raw, err := st.Open(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "no such file: it may have expired")
		return
	}
	filled, err := files.FillForm(raw, in.Values)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := st.Save(strings.TrimSuffix(f.Name, ".pdf")+" (filled).pdf", filled, files.OriginGenerated)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": saved.ID, "name": saved.Name, "size": saved.Size})
}

// ---- the agent's computer ----

const (
	computerTimeout  = 30 * time.Second
	maxCommand       = 4000
	maxReceipts      = 30
	maxReceiptOutput = 4000
	maxTextFile      = 256 << 10
)

// Receipt is one command the user ran on the agent's computer, and how it went.
type Receipt struct {
	At       time.Time `json:"at"`
	Command  string    `json:"command"`
	Output   string    `json:"output"`
	Exit     int       `json:"exit"`
	TimedOut bool      `json:"timed_out,omitempty"`
	Ms       int64     `json:"ms"`
}

// computer says whether the terminal and files are offered, and where they
// act. They are offered only in an environment apart from the machine Talaria
// runs on (the workspace container, another container, a host over SSH): a
// page that could run commands on the server itself would make the app's
// password a key to the whole machine.
func (s *Server) computer() (execenv.Env, string) {
	switch {
	case s.Env == nil:
		return nil, "No separate computer is set up. Start the workspace (tal workspace up) and make it the default environment to use it from here."
	case !s.Env.Remote():
		return nil, "The agent's tools run on the server itself here, so its terminal and files are not offered from the app. Start the workspace (tal workspace up) and set \"default_environment\": \"workspace\" to get a computer of its own."
	}
	return s.Env, ""
}

func (s *Server) scrub(text string) string {
	if s.Scrub != nil {
		return s.Scrub(text)
	}
	return text
}

func (s *Server) loadReceipts() []Receipt {
	var out []Receipt
	if s.ReceiptsPath == "" {
		return out
	}
	if raw, err := os.ReadFile(s.ReceiptsPath); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (s *Server) addReceipt(rc Receipt) {
	if s.ReceiptsPath == "" {
		return
	}
	s.receiptMu.Lock()
	defer s.receiptMu.Unlock()
	list := append(s.loadReceipts(), rc)
	if len(list) > maxReceipts {
		list = list[len(list)-maxReceipts:]
	}
	if raw, err := json.Marshal(list); err == nil {
		_ = writeFile(s.ReceiptsPath, raw)
	}
}

func (s *Server) computerGet(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	out := map[string]any{"available": env != nil, "reason": why, "receipts": []Receipt{}}
	if env != nil {
		out["where"] = env.Describe()
		list := s.loadReceipts()
		rev := make([]Receipt, len(list))
		for i, rc := range list {
			rev[len(list)-1-i] = rc
		}
		out["receipts"] = rev
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) computerRun(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	if env == nil {
		fail(w, http.StatusNotImplemented, why)
		return
	}
	var in struct {
		Command string `json:"command"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	cmd := strings.TrimSpace(in.Command)
	switch {
	case cmd == "":
		fail(w, http.StatusBadRequest, "there is no command to run")
		return
	case len(cmd) > maxCommand:
		fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a command is at most %d characters", maxCommand))
		return
	}
	started := time.Now()
	res, err := env.Shell(r.Context(), cmd, computerTimeout)
	if err != nil {
		fail(w, http.StatusBadGateway, s.scrub(err.Error()))
		return
	}
	output := s.scrub(res.Output)
	if res.Truncated {
		output += "\n[the output was cut here]"
	}
	if res.TimedOut {
		output += fmt.Sprintf("\n[stopped after %s]", computerTimeout)
	}
	rc := Receipt{At: started, Command: cmd, Output: clipRunes(output, maxReceiptOutput), Exit: res.ExitCode, TimedOut: res.TimedOut, Ms: time.Since(started).Milliseconds()}
	s.addReceipt(rc)
	rc.Output = output // the answer carries all of it; the saved receipt keeps the head
	writeJSON(w, http.StatusOK, rc)
}

// cleanPath accepts a path in the computer's own filesystem: no control
// characters, a sensible length. Empty is its working directory.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return ".", nil
	}
	if len(p) > 1024 {
		return "", errors.New("that path is too long")
	}
	for _, c := range p {
		if c < ' ' || c == 0x7f {
			return "", errors.New("a path cannot contain a control character")
		}
	}
	return path.Clean(p), nil
}

type dirEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
}

func (s *Server) computerFiles(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	if env == nil {
		fail(w, http.StatusNotImplemented, why)
		return
	}
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), computerTimeout)
	defer cancel()
	names, err := env.ListDir(ctx, p)
	if err != nil {
		fail(w, http.StatusBadRequest, s.scrub(err.Error()))
		return
	}
	const most = 500
	out := make([]dirEntry, 0, min(len(names), most))
	for _, n := range names {
		if len(out) == most {
			break
		}
		out = append(out, dirEntry{Name: strings.TrimSuffix(n, "/"), Dir: strings.HasSuffix(n, "/")})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out, "more": max(0, len(names)-most)})
}

func (s *Server) computerFileGet(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	if env == nil {
		fail(w, http.StatusNotImplemented, why)
		return
	}
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), computerTimeout)
	defer cancel()
	data, truncated, err := env.ReadFile(ctx, p, maxTextFile)
	if err != nil {
		fail(w, http.StatusBadRequest, s.scrub(err.Error()))
		return
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		fail(w, http.StatusUnsupportedMediaType, "That is not a text file. A PDF or an image can be moved to Documents instead.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "text": s.scrub(string(data)), "truncated": truncated})
}

// computerFilePost saves a text file, or makes a folder when Dir is set.
func (s *Server) computerFilePost(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	if env == nil {
		fail(w, http.StatusNotImplemented, why)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTextFile+(8<<10))
	var in struct {
		Path string `json:"path"`
		Text string `json:"text"`
		Dir  bool   `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "that file is too big to save from here, or could not be read")
		return
	}
	p, err := cleanPath(in.Path)
	if err != nil || p == "." {
		fail(w, http.StatusBadRequest, "say where to save it")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), computerTimeout)
	defer cancel()
	if in.Dir {
		res, err := env.Shell(ctx, "mkdir -p -- "+shellQuote(p), computerTimeout)
		if err == nil && res.ExitCode != 0 {
			err = errors.New(strings.TrimSpace(res.Output))
		}
		if err != nil {
			fail(w, http.StatusBadRequest, s.scrub(err.Error()))
			return
		}
	} else if err := env.WriteFile(ctx, p, []byte(in.Text)); err != nil {
		fail(w, http.StatusBadRequest, s.scrub(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p})
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// computerTransfer moves a PDF or an image between the computer and
// Documents: keep copies one out of the computer into the kept files, put
// copies a kept file into a folder of the computer.
func (s *Server) computerTransfer(w http.ResponseWriter, r *http.Request) {
	env, why := s.computer()
	if env == nil {
		fail(w, http.StatusNotImplemented, why)
		return
	}
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	var in struct {
		Keep string `json:"keep"` // a path on the computer
		Put  string `json:"put"`  // a kept file's id
		Path string `json:"path"` // with put: the folder to put it in
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*computerTimeout)
	defer cancel()
	switch {
	case strings.TrimSpace(in.Keep) != "":
		p, err := cleanPath(in.Keep)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		data, truncated, err := env.ReadFile(ctx, p, files.MaxBytes+1)
		if err != nil {
			fail(w, http.StatusBadRequest, s.scrub(err.Error()))
			return
		}
		if truncated || len(data) > files.MaxBytes {
			fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("that file is bigger than %d MB", files.MaxBytes>>20))
			return
		}
		f, err := st.Save(path.Base(p), data, files.OriginUpload)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": f.ID, "name": f.Name, "size": f.Size})
	case strings.TrimSpace(in.Put) != "":
		dir, err := cleanPath(in.Path)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		f, data, err := st.Open(strings.TrimSpace(in.Put))
		if err != nil {
			fail(w, http.StatusNotFound, "no such file: it may have expired")
			return
		}
		dest := path.Join(dir, path.Base(strings.ReplaceAll(f.Name, "\\", "/")))
		if err := env.WriteFile(ctx, dest, data); err != nil {
			fail(w, http.StatusBadRequest, s.scrub(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": dest})
	default:
		fail(w, http.StatusBadRequest, "say what to move: keep a path, or put a file id")
	}
}

// ---- the agent's browser, in the user's hands ----

// BrowserTab is a tab the agent has open.
type BrowserTab struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Login says the page is asking for a password now, so the app offers its
	// sign-in box (username and password typed straight into the page).
	Login bool `json:"login"`
}

func (s *Server) managers() []*browser.Manager {
	if s.Browsers == nil {
		return nil
	}
	return s.Browsers()
}

// tab finds an open tab by the id the list gave. It never starts a browser or
// opens a tab: taking control is of what the agent already has open.
func (s *Server) tab(id string) (*browser.Manager, *browser.Page) {
	n, owner, ok := strings.Cut(id, ".")
	i, err := strconv.Atoi(n)
	ms := s.managers()
	if !ok || err != nil || i < 0 || i >= len(ms) {
		return nil, nil
	}
	return ms[i], ms[i].Peek(owner)
}

func (s *Server) browserTabs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tabs := []BrowserTab{}
	for i, m := range s.managers() {
		for _, owner := range m.Owners() {
			if p := m.Peek(owner); p != nil {
				tabs = append(tabs, BrowserTab{ID: strconv.Itoa(i) + "." + owner, URL: p.Where(ctx), Login: p.HasPassword(ctx)})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tabs": tabs, "width": browser.Width, "height": browser.Height})
}

// browserShot is a picture of a tab, taken now. Nothing is cached: a picture
// exists only for as long as it takes to send.
func (s *Server) browserShot(w http.ResponseWriter, r *http.Request) {
	_, p := s.tab(r.URL.Query().Get("id"))
	if p == nil {
		fail(w, http.StatusNotFound, "that tab is no longer open")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	img, err := p.Shot(ctx, 55)
	if err != nil {
		fail(w, http.StatusBadGateway, "the browser did not answer")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Page-URL", p.Where(ctx))
	w.Write(img)
}

func (s *Server) browserAct(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string  `json:"id"`
		Kind     string  `json:"kind"` // click, type, replace, key, wheel, open, back, login
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		DY       float64 `json:"dy"`
		Text     string  `json:"text"`
		Key      string  `json:"key"`
		URL      string  `json:"url"`
		Username string  `json:"username"` // login only: the user's own, typed into the page and never kept
		Password string  `json:"password"` // login only: likewise, and never echoed back
	}
	if !readJSON(w, r, &in) {
		return
	}
	m, p := s.tab(in.ID)
	if p == nil {
		fail(w, http.StatusNotFound, "that tab is no longer open")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var err error
	switch in.Kind {
	case "click":
		err = p.ClickAt(ctx, in.X, in.Y)
	case "type":
		err = p.InsertText(ctx, in.Text)
	case "replace":
		err = p.ReplaceText(ctx, in.Text)
	case "key":
		err = p.PressKey(ctx, in.Key)
	case "wheel":
		err = p.Wheel(ctx, in.X, in.Y, in.DY)
	case "open":
		err = m.Go(ctx, p, in.URL)
	case "back":
		err = p.GoBack(ctx)
	case "login":
		err = p.FillLogin(ctx, in.Username, in.Password)
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"ok": true, "url": p.Where(ctx)}
	if in.Kind == "click" || in.Kind == "key" || in.Kind == "login" { // what now has focus, so the page can offer the right box to type into (Tab can move it to a password field)
		out["focus"] = p.FocusKind(ctx)
	}
	writeJSON(w, http.StatusOK, out)
}
