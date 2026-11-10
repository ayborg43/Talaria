package files

import (
	"errors"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Make turns what the agent writes into a finished page: a document, a web page, a slide deck or a
// dashboard. Each comes out as one self-contained HTML file with no script, no outside address and no font to
// fetch, so it opens anywhere, works offline and can be sent as it is. It is also served under a policy
// that forbids scripts and network access altogether (see the web server), so what the agent writes into a
// page can only ever be a page.

// Limits keep a made page a few hundred kilobytes at most.
const (
	maxMakeBytes  = 600 << 10
	maxSlides     = 60
	maxBullets    = 12
	maxMetrics    = 12
	maxCharts     = 6
	maxPoints     = 60
	maxTableRows  = 60
	maxTableCols  = 8
	maxTitleRunes = 120
)

// MIMEMade is the type of a made page.
const MIMEMade = "text/html"

const baseCSS = `:root{color-scheme:light dark;--bg:#fbfaf7;--ink:#1c1a16;--muted:#6b665c;--line:#e4e0d6;--card:#fff;--accent:#9a6b1f;--a2:#2f6f8f;--a3:#3d7a4f;--a4:#a8503f}
@media (prefers-color-scheme:dark){:root{--bg:#14120f;--ink:#f0ece3;--muted:#a39d90;--line:#2e2a23;--card:#1c1914;--accent:#d9a441;--a2:#6fb0d0;--a3:#78b88b;--a4:#df8a78}}
*{box-sizing:border-box}html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--ink);font:17px/1.65 Georgia,"Iowan Old Style","Times New Roman",serif}
main{max-width:720px;margin:0 auto;padding:40px 20px 80px}
h1,h2,h3,h4{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;line-height:1.25;margin:1.6em 0 .5em}
h1{font-size:2rem;margin-top:0}h2{font-size:1.45rem}h3{font-size:1.15rem}
p,ul,ol,blockquote,pre,table{margin:0 0 1em}
a{color:var(--a2)}code{font:.9em ui-monospace,Menlo,monospace;background:var(--card);border:1px solid var(--line);border-radius:4px;padding:.05em .3em}
pre{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 14px;overflow:auto}pre code{border:0;padding:0;background:none}
blockquote{border-left:3px solid var(--accent);margin-left:0;padding:0 0 0 14px;color:var(--muted)}
hr{border:0;border-top:1px solid var(--line);margin:2em 0}
table{border-collapse:collapse;width:100%;font:15px/1.4 system-ui,sans-serif}th,td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:left;vertical-align:top}th{color:var(--muted);font-weight:600}
@media print{body{background:#fff;color:#000}main{padding:0}}`

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func checkTitle(title string) (string, error) {
	t := clipRunes(title, maxTitleRunes)
	if t == "" {
		return "", errors.New("it needs a title")
	}
	return t, nil
}

func shell(title, css, body string) []byte {
	return []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + escapeText(title) + `</title>
<style>` + baseCSS + css + `</style></head>
<body>` + body + `</body></html>
`)
}

func tooBig(b []byte) error {
	if len(b) > maxMakeBytes {
		return fmt.Errorf("that comes to %d KB; a made page is at most %d KB: make it shorter or split it", len(b)>>10, maxMakeBytes>>10)
	}
	return nil
}

// RenderDoc makes a document from a title and Markdown.
func RenderDoc(title, markdown string) ([]byte, error) {
	t, err := checkTitle(title)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(markdown) == "" {
		return nil, errors.New("a document needs some text")
	}
	if len(markdown) > maxMakeBytes/2 {
		return nil, errors.New("the text is too long for one document: split it")
	}
	out := shell(t, "", "<main>\n<h1>"+escapeText(t)+"</h1>\n"+markdownToHTML(markdown)+"</main>")
	return out, tooBig(out)
}

var (
	// Tags a page may not contain: they run code, load something, or reach out. The serving policy forbids all
	// of it anyway; removing them keeps the file harmless when it is opened somewhere else.
	badBlock = regexp.MustCompile(`(?is)<\s*(script|iframe|object|embed|applet|frame|frameset|form|template|svg\s+[^>]*onload)\b.*?(</\s*(script|iframe|object|embed|applet|frame|frameset|form|template)\s*>|$)`)
	badTag   = regexp.MustCompile(`(?i)<\s*/?\s*(script|iframe|object|embed|applet|frame|frameset|link|meta|base|form|input|button|textarea|select|template|html|head|body|title)\b[^>]*>`)
	badAttr  = regexp.MustCompile(`(?i)\s(on\w+|srcdoc|formaction|ping)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	badURL   = regexp.MustCompile(`(?i)\s(href|src|xlink:href|action|poster|data)\s*=\s*("\s*(javascript|vbscript|data:text|file):[^"]*"|'\s*(javascript|vbscript|data:text|file):[^']*')`)
	cssLoad  = regexp.MustCompile(`(?i)@import[^;]*;?|url\(\s*['"]?\s*(https?:)?//[^)]*\)|expression\s*\(`)
	bodyOf   = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
	styleOf  = regexp.MustCompile(`(?is)<style[^>]*>(.*?)</style>`)
)

// RenderPage makes a web page from HTML the agent wrote: a fragment or a whole document. Scripts, frames,
// forms, event handlers and links that run code are removed, styles are kept (without anything they would
// fetch), and the result sits in a plain responsive shell.
func RenderPage(title, markup string) ([]byte, error) {
	t, err := checkTitle(title)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(markup) == "" {
		return nil, errors.New("a page needs some HTML")
	}
	if len(markup) > maxMakeBytes/2 {
		return nil, errors.New("the page is too long: make it shorter")
	}
	var css strings.Builder
	for _, m := range styleOf.FindAllStringSubmatch(markup, -1) {
		css.WriteString(cssLoad.ReplaceAllString(strings.ReplaceAll(m[1], "</", "<\\/"), "") + "\n")
	}
	body := markup
	if m := bodyOf.FindStringSubmatch(body); m != nil {
		body = m[1]
	}
	body = styleOf.ReplaceAllString(body, "")
	body = badBlock.ReplaceAllString(body, "")
	body = badTag.ReplaceAllString(body, "")
	body = badAttr.ReplaceAllString(body, "")
	body = badURL.ReplaceAllString(body, "")
	// Every element's inline style gets the same treatment as a style block.
	body = regexp.MustCompile(`(?i)(style\s*=\s*"[^"]*)`).ReplaceAllStringFunc(body, func(s string) string { return cssLoad.ReplaceAllString(s, "") })
	out := shell(t, "main{max-width:none;padding:0}\n"+css.String(), "<main>\n"+body+"\n</main>")
	return out, tooBig(out)
}

// Slide is one slide of a deck.
type Slide struct {
	Title   string   `json:"title"`
	Bullets []string `json:"bullets,omitempty"`
	Text    string   `json:"text,omitempty"`  // a sentence or short paragraph, shown under the title
	Notes   string   `json:"notes,omitempty"` // speaker notes: shown small under the slide, not in print
}

const slidesCSS = `html{scroll-snap-type:y mandatory;scroll-behavior:smooth}
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
.slide{min-height:100vh;min-height:100dvh;scroll-snap-align:start;display:flex;flex-direction:column;justify-content:center;padding:6vh 8vw;border-bottom:1px solid var(--line);position:relative}
.slide h1,.slide h2{margin:0 0 .5em;font-size:clamp(1.8rem,5vw,3.2rem);letter-spacing:-.01em}
.slide.title h1{font-size:clamp(2.4rem,7vw,4.6rem);border-left:6px solid var(--accent);padding-left:.4em}
.slide p.lead{font-size:clamp(1.1rem,2.4vw,1.7rem);color:var(--muted);margin:0 0 1em;max-width:34em}
.slide ul{font-size:clamp(1.1rem,2.6vw,1.8rem);line-height:1.5;padding-left:1.1em;max-width:30em}
.slide li{margin:.3em 0}
.slide .n{position:absolute;right:4vw;bottom:3vh;color:var(--muted);font-size:.9rem}
.slide aside{margin-top:1.4em;max-width:40em;color:var(--muted);font-size:.95rem;border-top:1px dashed var(--line);padding-top:.6em}
.hint{position:fixed;left:50%;bottom:10px;transform:translateX(-50%);font-size:12px;color:var(--muted);opacity:.8}
@media print{html{scroll-snap-type:none}.slide{min-height:0;height:100vh;page-break-after:always;border:0}.hint,.slide aside{display:none}}`

// RenderSlides makes a deck: one full-screen slide per entry, scrolled or moved through with the arrow keys,
// space or the page keys, and printed one slide to a page.
func RenderSlides(title string, slides []Slide) ([]byte, error) {
	t, err := checkTitle(title)
	if err != nil {
		return nil, err
	}
	if len(slides) == 0 {
		return nil, errors.New("a deck needs at least one slide")
	}
	if len(slides) > maxSlides {
		return nil, fmt.Errorf("a deck is at most %d slides", maxSlides)
	}
	var b strings.Builder
	b.WriteString(`<div class="hint">arrow keys or swipe</div>` + "\n")
	for i, s := range slides {
		st, err := checkTitle(s.Title)
		if err != nil {
			return nil, fmt.Errorf("slide %d: %w", i+1, err)
		}
		class := "slide"
		tag := "h2"
		if i == 0 {
			class, tag = "slide title", "h1"
		}
		fmt.Fprintf(&b, `<section class="%s" id="s%d" tabindex="0"><%s>%s</%s>`, class, i+1, tag, escapeText(st), tag)
		if txt := clipRunes(s.Text, 400); txt != "" {
			b.WriteString(`<p class="lead">` + inline(txt) + `</p>`)
		}
		if len(s.Bullets) > maxBullets {
			return nil, fmt.Errorf("slide %d has %d bullets; at most %d fit", i+1, len(s.Bullets), maxBullets)
		}
		var items []string
		for _, bl := range s.Bullets {
			if bl = clipRunes(bl, 240); bl != "" {
				items = append(items, "<li>"+inline(bl)+"</li>")
			}
		}
		if len(items) > 0 {
			b.WriteString("<ul>" + strings.Join(items, "") + "</ul>")
		}
		if n := clipRunes(s.Notes, 600); n != "" {
			b.WriteString("<aside>" + inline(n) + "</aside>")
		}
		fmt.Fprintf(&b, `<span class="n">%d / %d</span></section>`+"\n", i+1, len(slides))
	}
	out := shell(t, slidesCSS, b.String())
	return out, tooBig(out)
}

// Metric is one headline number on a dashboard.
type Metric struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Delta string `json:"delta,omitempty"` // "+4.2%": a leading + or - colours it
	Note  string `json:"note,omitempty"`
}

// Point is one value in a chart.
type Point struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Chart is a bar or line chart of labelled values.
type Chart struct {
	Title  string  `json:"title"`
	Kind   string  `json:"kind"` // "bar" (the default) or "line"
	Unit   string  `json:"unit,omitempty"`
	Points []Point `json:"points"`
}

// Table is a small table of text cells.
type Table struct {
	Title   string     `json:"title,omitempty"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// Dashboard is what RenderDashboard draws.
type Dashboard struct {
	Subtitle string   `json:"subtitle,omitempty"`
	Metrics  []Metric `json:"metrics,omitempty"`
	Charts   []Chart  `json:"charts,omitempty"`
	Tables   []Table  `json:"tables,omitempty"`
}

const dashCSS = `main{max-width:1000px}body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
.sub{color:var(--muted);margin:-.2em 0 1.4em}
.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px;margin-bottom:20px}
.metric,.panel{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:14px 16px}
.metric .l{color:var(--muted);font-size:.85rem}.metric .v{font-size:1.9rem;font-weight:650;letter-spacing:-.02em;line-height:1.2}
.metric .d{font-size:.85rem;font-weight:600}.up{color:var(--a3)}.down{color:var(--a4)}.metric .n{color:var(--muted);font-size:.8rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,440px),1fr));gap:12px;margin-bottom:12px}
.panel h2{font-size:1rem;margin:0 0 10px}.panel svg{width:100%;height:auto;display:block}
.panel text{fill:var(--muted);font:11px system-ui,sans-serif}.panel .val{fill:var(--ink)}
.panel .bar{fill:var(--accent)}.panel .ln{fill:none;stroke:var(--a2);stroke-width:2.2;stroke-linejoin:round}.panel .dot{fill:var(--a2)}.panel .gl{stroke:var(--line)}
@media print{.metric,.panel{break-inside:avoid}}`

// num writes a chart value compactly: 1,250,000 as 1.25M, 0.5 as 0.5.
func num(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return trim(v/1e9) + "B"
	case a >= 1e6:
		return trim(v/1e6) + "M"
	case a >= 1e4:
		return trim(v/1e3) + "K"
	}
	return trim(v)
}
func trim(v float64) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	return s
}

func barChart(c Chart) string {
	pts := c.Points
	max := 0.0
	for _, p := range pts {
		max = math.Max(max, math.Abs(p.Value))
	}
	if max == 0 {
		max = 1
	}
	const rowH, labelW, valW, barW = 26, 130.0, 70.0, 330.0
	h := len(pts)*rowH + 6
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" role="img" aria-label="%s">`, int(labelW+barW+valW), h, html.EscapeString(c.Title))
	for i, p := range pts {
		y := i*rowH + 3
		w := math.Abs(p.Value) / max * barW
		fmt.Fprintf(&b, `<text x="0" y="%d">%s</text><rect class="bar" x="%.0f" y="%d" width="%.1f" height="16" rx="3"/><text class="val" x="%.1f" y="%d">%s</text>`,
			y+12, escapeText(clipRunes(p.Label, 20)), labelW, y, w, labelW+w+6, y+12, escapeText(num(p.Value)+c.Unit))
	}
	return b.String() + `</svg>`
}

func lineChart(c Chart) string {
	pts := c.Points
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
	}
	if lo > 0 {
		lo = 0 // a line that starts above zero exaggerates its changes
	}
	if hi == lo {
		hi = lo + 1
	}
	const W, H, L, R, T, B = 560.0, 220.0, 46.0, 12.0, 10.0, 26.0
	x := func(i int) float64 {
		if len(pts) == 1 {
			return L + (W-L-R)/2
		}
		return L + float64(i)*(W-L-R)/float64(len(pts)-1)
	}
	y := func(v float64) float64 { return T + (hi-v)/(hi-lo)*(H-T-B) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`, W, H, html.EscapeString(c.Title))
	for k := 0; k <= 4; k++ {
		v := lo + (hi-lo)*float64(k)/4
		fmt.Fprintf(&b, `<line class="gl" x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f"/><text x="%.0f" y="%.1f" text-anchor="end">%s</text>`, L, W-R, y(v), y(v), L-6, y(v)+4, escapeText(num(v)))
	}
	var path []string
	for i, p := range pts {
		path = append(path, fmt.Sprintf("%.1f,%.1f", x(i), y(p.Value)))
	}
	fmt.Fprintf(&b, `<polyline class="ln" points="%s"/>`, strings.Join(path, " "))
	for i, p := range pts {
		fmt.Fprintf(&b, `<circle class="dot" cx="%.1f" cy="%.1f" r="2.8"><title>%s: %s</title></circle>`, x(i), y(p.Value), escapeText(p.Label), escapeText(num(p.Value)+c.Unit))
	}
	step := max(1, (len(pts)+5)/6) // about six labels along the bottom
	for i := 0; i < len(pts); i += step {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, x(i), H-8, escapeText(clipRunes(pts[i].Label, 10)))
	}
	return b.String() + `</svg>`
}

// RenderDashboard makes a dashboard: headline numbers, bar and line charts drawn as inline SVG, and small
// tables, from data the agent gives it. It draws what it is given and works out nothing.
func RenderDashboard(title string, d Dashboard) ([]byte, error) {
	t, err := checkTitle(title)
	if err != nil {
		return nil, err
	}
	if len(d.Metrics)+len(d.Charts)+len(d.Tables) == 0 {
		return nil, errors.New("a dashboard needs at least one metric, chart or table")
	}
	if len(d.Metrics) > maxMetrics || len(d.Charts) > maxCharts || len(d.Tables) > 4 {
		return nil, fmt.Errorf("a dashboard holds at most %d metrics, %d charts and 4 tables", maxMetrics, maxCharts)
	}
	var b strings.Builder
	b.WriteString("<main>\n<h1>" + escapeText(t) + "</h1>\n")
	if sub := clipRunes(d.Subtitle, 200); sub != "" {
		b.WriteString(`<p class="sub">` + escapeText(sub) + "</p>\n")
	}
	if len(d.Metrics) > 0 {
		b.WriteString(`<div class="metrics">`)
		for _, m := range d.Metrics {
			delta := ""
			if dl := clipRunes(m.Delta, 20); dl != "" {
				class := ""
				switch dl[0] {
				case '+':
					class = " up"
				case '-':
					class = " down"
				}
				delta = `<div class="d` + class + `">` + escapeText(dl) + `</div>`
			}
			note := ""
			if n := clipRunes(m.Note, 80); n != "" {
				note = `<div class="n">` + escapeText(n) + `</div>`
			}
			b.WriteString(`<div class="metric"><div class="l">` + escapeText(clipRunes(m.Label, 40)) + `</div><div class="v">` + escapeText(clipRunes(m.Value, 24)) + `</div>` + delta + note + `</div>`)
		}
		b.WriteString("</div>\n")
	}
	if len(d.Charts) > 0 {
		b.WriteString(`<div class="grid">`)
		for i, c := range d.Charts {
			if len(c.Points) == 0 || len(c.Points) > maxPoints {
				return nil, fmt.Errorf("chart %d needs between 1 and %d points", i+1, maxPoints)
			}
			for _, p := range c.Points {
				if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
					return nil, fmt.Errorf("chart %d has a value that is not a number", i+1)
				}
			}
			c.Unit = clipRunes(c.Unit, 6)
			svg := barChart(c)
			if strings.EqualFold(c.Kind, "line") {
				svg = lineChart(c)
			}
			b.WriteString(`<div class="panel"><h2>` + escapeText(clipRunes(c.Title, 80)) + `</h2>` + svg + `</div>`)
		}
		b.WriteString("</div>\n")
	}
	for i, tb := range d.Tables {
		if len(tb.Columns) == 0 || len(tb.Columns) > maxTableCols || len(tb.Rows) > maxTableRows {
			return nil, fmt.Errorf("table %d needs 1 to %d columns and at most %d rows", i+1, maxTableCols, maxTableRows)
		}
		b.WriteString(`<div class="panel" style="margin-bottom:12px">`)
		if tt := clipRunes(tb.Title, 80); tt != "" {
			b.WriteString("<h2>" + escapeText(tt) + "</h2>")
		}
		b.WriteString("<table><thead><tr>")
		for _, c := range tb.Columns {
			b.WriteString("<th>" + escapeText(clipRunes(c, 40)) + "</th>")
		}
		b.WriteString("</tr></thead><tbody>")
		for _, row := range tb.Rows {
			b.WriteString("<tr>")
			for j := range tb.Columns {
				cell := ""
				if j < len(row) {
					cell = row[j]
				}
				b.WriteString("<td>" + escapeText(clipRunes(cell, 120)) + "</td>")
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</tbody></table></div>\n")
	}
	b.WriteString("</main>")
	out := shell(t, dashCSS, b.String())
	return out, tooBig(out)
}
