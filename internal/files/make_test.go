package files

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func TestMarkdownCannotWriteATagOrARunnableLink(t *testing.T) {
	out := markdownToHTML("# Hi <script>alert(1)</script>\n\n[x](javascript:alert(1)) and [ok](https://example.com/?a=1&b=\"2\")\n\n<img src=x onerror=alert(1)>\n\n```\n<b>raw</b>\n```")
	for _, bad := range []string{"<script", "<img", `href="javascript`, "onerror=alert(1)>", "<b>raw"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q came through: %s", bad, out)
		}
	}
	if !strings.Contains(out, `<a href="https://example.com/?a=1&amp;b=&#34;2&#34;" rel="noopener noreferrer">ok</a>`) {
		t.Errorf("a plain link was not made: %s", out)
	}
}

func TestMarkdownShapes(t *testing.T) {
	out := markdownToHTML("## Plan\n\nSome **bold** and *soft* and `code`.\n\n- one\n- two\n\n1. first\n2. second\n\n> quoted\n\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	for _, want := range []string{"<h2>Plan</h2>", "<strong>bold</strong>", "<em>soft</em>", "<code>code</code>", "<ul>", "<li>two</li>", "<ol>", "<blockquote>", "<hr>", "<th>a</th>", "<td>2</td>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestAPageLosesWhatCouldRunOrFetch(t *testing.T) {
	page, err := RenderPage("Landing", `<html><head><title>x</title><style>@import url(https://evil.example/a.css); h1{color:red} p{background:url(https://evil.example/p.png)}</style><script>steal()</script></head>
<body onload="x()"><h1 onclick="evil()">Hello</h1><a href="javascript:alert(1)">bad</a><a href="https://ok.example">good</a>
<iframe src="https://evil.example"></iframe><form action="https://evil.example"><input name=q></form><img src="x" onerror="evil()"><svg onload="evil()"></svg>
<script>
more()</script><p>after</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	for _, bad := range []string{"<script", "steal()", "more()", "onclick", "onerror", "onload", "javascript:", "<iframe", "<form", "<input", "@import", "evil.example"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q survived: %s", bad, s)
		}
	}
	for _, want := range []string{"<h1>Hello</h1>", `href="https://ok.example"`, "color:red", "<p>after</p>", "<title>Landing</title>"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q was lost: %s", want, s)
		}
	}
}

func TestSlidesEscapeAndAreBounded(t *testing.T) {
	page, err := RenderSlides("Q3 <review>", []Slide{{Title: "Q3 <review>", Text: "hello"}, {Title: "Numbers", Bullets: []string{"Up <b>12%</b>", "Down"}, Notes: "say it slowly"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if strings.Contains(s, "<review>") || strings.Contains(s, "<b>12%") || strings.Contains(s, "<script") {
		t.Fatalf("a slide let markup through: %s", s)
	}
	if strings.Count(s, `<section`) != 2 || !strings.Contains(s, "2 / 2") || !strings.Contains(s, "<h1>") {
		t.Fatalf("slides are wrong: %s", s)
	}
	if _, err := RenderSlides("x", nil); err == nil {
		t.Error("an empty deck was made")
	}
	if _, err := RenderSlides("x", make([]Slide, maxSlides+1)); err == nil {
		t.Error("an enormous deck was made")
	}
	if _, err := RenderSlides("x", []Slide{{Title: "a", Bullets: make([]string, maxBullets+1)}}); err == nil {
		t.Error("a slide with too many bullets was made")
	}
}

func TestDashboardDrawsWhatItIsGivenAndRefusesWhatItCannot(t *testing.T) {
	d := Dashboard{
		Metrics: []Metric{{Label: "Revenue <x>", Value: "$1.2M", Delta: "+4%"}, {Label: "Churn", Value: "2%", Delta: "-0.3"}},
		Charts:  []Chart{{Title: "By region", Points: []Point{{"EMEA", 42}, {"<US>", 58}}}, {Title: "Trend", Kind: "line", Unit: "%", Points: []Point{{"Jan", 1}, {"Feb", 3}, {"Mar", 2}}}},
		Tables:  []Table{{Columns: []string{"Name", "Owner"}, Rows: [][]string{{"Alpha", "<i>me</i>"}}}},
	}
	page, err := RenderDashboard("Sales", d)
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	for _, want := range []string{`class="metric"`, `class="d up"`, `class="d down"`, "<rect", "<polyline", "<table>", "$1.2M"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"<x>", "<US>", "<i>me", "<script"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q came through unescaped", bad)
		}
	}
	if _, err := RenderDashboard("x", Dashboard{}); err == nil {
		t.Error("an empty dashboard was made")
	}
	if _, err := RenderDashboard("x", Dashboard{Charts: []Chart{{Title: "bad", Points: []Point{{"a", math.NaN()}}}}}); err == nil {
		t.Error("a chart with NaN was drawn")
	}
	if _, err := RenderDashboard("x", Dashboard{Charts: []Chart{{Title: "none"}}}); err == nil {
		t.Error("a chart with no points was drawn")
	}
}

func TestAMadePageIsKeptAndAnUploadCannotBeOne(t *testing.T) {
	s := testStore(t)
	page, _ := RenderDoc("Memo", "Hello")
	f, err := s.SaveMade("Memo.html", page, KindDoc)
	if err != nil || f.MIME != MIMEMade || f.Origin != OriginGenerated || f.Kind != KindDoc {
		t.Fatalf("%+v %v", f, err)
	}
	if got, raw, err := s.Open(f.ID); err != nil || got.Name != "Memo.html" || !strings.Contains(string(raw), "<h1>Memo</h1>") {
		t.Fatalf("the page did not come back: %v", err)
	}
	if _, err := s.SaveMade("x.html", page, "script"); err == nil {
		t.Fatal("a made page of an unknown kind was kept")
	}
	if _, err := s.Save("evil.html", []byte("<!doctype html><script>alert(1)</script>"), OriginUpload); err == nil {
		t.Fatal("an uploaded page was kept")
	}
	if Detect([]byte("<!doctype html><html></html>")) != "" {
		t.Fatal("HTML was detected as a keepable type")
	}
}

func TestTheMakeToolsKeepThePageAndReturnARecord(t *testing.T) {
	s := testStore(t)
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s})
	for name, args := range map[string]string{
		"files_make_doc":       `{"title":"Plan","markdown":"# Plan\n\n- a\n- b"}`,
		"files_make_page":      `{"title":"Card","html":"<h1>Hi</h1><style>h1{color:teal}</style>"}`,
		"files_make_slides":    `{"title":"Deck","slides":[{"title":"Deck"},{"title":"Two","bullets":["x"]}]}`,
		"files_make_dashboard": `{"title":"Dash","metrics":[{"label":"Users","value":"12"}]}`,
	} {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		res, err := tl.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var rec struct{ Kind, ID, Name, MIME string }
		if err := json.Unmarshal(res.Artifact, &rec); err != nil || rec.Kind != "document" || rec.MIME != MIMEMade || !strings.HasSuffix(rec.Name, ".html") {
			t.Fatalf("%s: record %s (%v)", name, res.Artifact, err)
		}
		if _, _, err := s.Open(rec.ID); err != nil {
			t.Fatalf("%s: the record names a file that is not kept: %v", name, err)
		}
	}
	tl, _ := reg.Get("files_make_doc")
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"title":"","markdown":"x"}`)); err == nil {
		t.Error("a document with no title was made")
	}
}
