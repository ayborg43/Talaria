package files

import (
	"context"
	"encoding/json"
	"fmt"

	"talaria/internal/tool"
)

const madeNote = " It is one self-contained page (no script, no outside links needed) that opens in the app and can be saved or shared. " +
	"Say the link exactly as returned so the user can tap it, and tell them what you made in a sentence: do not paste its contents back."

// registerMake adds the tools that make documents, pages, slide decks and dashboards.
func registerMake(r *tool.Registry, b Binding) {
	r.Register(tool.Tool{
		Name: "files_make_doc",
		Description: "Make a formatted document (a report, plan, brief, memo or notes) from a title and Markdown: headings, paragraphs, lists, tables, quotes, code and links." + madeNote +
			" For a printable file use files_make_pdf instead.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string"},
			"markdown":{"type":"string","description":"the document body in Markdown: # headings, - lists, | tables |, **bold**, [links](https://...)"}},
			"required":["title","markdown"]}`),
		Run:     makeTool(b, "doc"),
		RunRich: b.withDocument(makeTool(b, "doc")),
	})
	r.Register(tool.Tool{
		Name: "files_make_page",
		Description: "Make a web page from HTML and CSS you write: a landing page, a one-pager, a card, an invitation, a styled table. Give a body fragment or a whole document, with <style> for the look." + madeNote +
			" Scripts, forms, frames and anything fetched from the internet are removed, so make the page work as plain HTML and CSS, with images as data: addresses or drawn with CSS and inline SVG.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string"},
			"html":{"type":"string","description":"the page's HTML, with a <style> block for the design"}},
			"required":["title","html"]}`),
		Run:     makeTool(b, "page"),
		RunRich: b.withDocument(makeTool(b, "page")),
	})
	r.Register(tool.Tool{
		Name: "files_make_slides",
		Description: "Make a slide deck: the first slide is the title slide, then one slide per entry, each with a title, optional bullets, optional short text and optional speaker notes. " + madeNote +
			" Keep slides sparse: at most about six short bullets each.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string"},
			"slides":{"type":"array","items":{"type":"object","properties":{
				"title":{"type":"string"},
				"text":{"type":"string","description":"a sentence under the title"},
				"bullets":{"type":"array","items":{"type":"string"}},
				"notes":{"type":"string","description":"speaker notes"}},
				"required":["title"]}}},
			"required":["title","slides"]}`),
		Run:     makeTool(b, "slides"),
		RunRich: b.withDocument(makeTool(b, "slides")),
	})
	r.Register(tool.Tool{
		Name:        "files_make_dashboard",
		Description: "Make a dashboard from data you already have: headline numbers, bar or line charts and small tables. It draws exactly what it is given and computes nothing, so work the numbers out first (from a calculation, a file or a source you read) and never invent any." + madeNote,
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string"},
			"subtitle":{"type":"string","description":"where the numbers come from and as of when"},
			"metrics":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"value":{"type":"string"},"delta":{"type":"string","description":"such as +4.2% or -3"},"note":{"type":"string"}},"required":["label","value"]}},
			"charts":{"type":"array","items":{"type":"object","properties":{"title":{"type":"string"},"kind":{"type":"string","enum":["bar","line"]},"unit":{"type":"string"},
				"points":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"value":{"type":"number"}},"required":["label","value"]}}},"required":["title","points"]}},
			"tables":{"type":"array","items":{"type":"object","properties":{"title":{"type":"string"},"columns":{"type":"array","items":{"type":"string"}},"rows":{"type":"array","items":{"type":"array","items":{"type":"string"}}}},"required":["columns","rows"]}}},
			"required":["title"]}`),
		Run:     makeTool(b, "dashboard"),
		RunRich: b.withDocument(makeTool(b, "dashboard")),
	})
}

// makeTool builds the Run function of one make tool: it reads the arguments, renders the page, keeps it and says where it is.
func makeTool(b Binding, kind string) func(context.Context, json.RawMessage) (string, error) {
	return func(_ context.Context, raw json.RawMessage) (string, error) {
		var page []byte
		var title string
		var err error
		switch kind {
		case "doc":
			var a struct{ Title, Markdown string }
			if err = json.Unmarshal(raw, &a); err == nil {
				title = a.Title
				page, err = RenderDoc(a.Title, a.Markdown)
			}
		case "page":
			var a struct{ Title, HTML string }
			if err = json.Unmarshal(raw, &a); err == nil {
				title = a.Title
				page, err = RenderPage(a.Title, a.HTML)
			}
		case "slides":
			var a struct {
				Title  string
				Slides []Slide
			}
			if err = json.Unmarshal(raw, &a); err == nil {
				title = a.Title
				page, err = RenderSlides(a.Title, a.Slides)
			}
		case "dashboard":
			var a struct {
				Title string
				Dashboard
			}
			if err = json.Unmarshal(raw, &a); err == nil {
				title = a.Title
				page, err = RenderDashboard(a.Title, a.Dashboard)
			}
		}
		if err != nil {
			return "", err
		}
		f, err := b.Store.SaveMade(clipRunes(title, 60)+".html", page)
		if err != nil {
			return "", err
		}
		if h, ok := b.retention(); ok {
			return fmt.Sprintf("%s is ready (%d KB) — open it at %s before it expires in %d hours.", f.Name, max(1, int(f.Size>>10)), link(f.ID), h), nil
		}
		return fmt.Sprintf("%s is ready (%d KB) — open it at %s before it expires.", f.Name, max(1, int(f.Size>>10)), link(f.ID)), nil
	}
}
