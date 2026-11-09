package calendar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DAVClient writes events to one CalDAV collection: PUT to create or replace
// an event resource, DELETE to remove it, guarded by ETags so a write never
// silently overwrites a change made elsewhere (If-Match, and If-None-Match
// on create). Credentials are the calendar account's, used only as HTTP
// basic auth and never written into errors.
type DAVClient struct {
	Collection string // the collection address, ending in /
	Username   string
	Password   string
	Client     *http.Client // nil uses a plain 20s client; tests inject one
}

func (c *DAVClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *DAVClient) url(uid string) string {
	return strings.TrimSuffix(c.Collection, "/") + "/" + uid + ".ics"
}

func (c *DAVClient) do(ctx context.Context, method, uid string, body []byte, headers map[string]string) (int, string, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(uid), rdr)
	if err != nil {
		return 0, "", nil, fmt.Errorf("the CalDAV address is not usable")
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	if body != nil {
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("the calendar server could not be reached")
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header.Get("ETag"), out, nil
}

// Put creates the event when etag is empty (refusing to overwrite an
// existing one) or replaces it only if etag still matches the server's.
func (c *DAVClient) Put(ctx context.Context, uid string, ics []byte, etag string) (string, error) {
	h := map[string]string{}
	if etag == "" {
		h["If-None-Match"] = "*"
	} else {
		h["If-Match"] = etag
	}
	code, newETag, _, err := c.do(ctx, http.MethodPut, uid, ics, h)
	if err != nil {
		return "", err
	}
	switch code {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return newETag, nil
	case http.StatusPreconditionFailed:
		return "", fmt.Errorf("the event changed on the server since it was read: read it again and retry")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("the calendar server refused the login (HTTP %d)", code)
	default:
		return "", fmt.Errorf("the calendar server answered HTTP %d to the write", code)
	}
}

// Get reads one event and its ETag.
func (c *DAVClient) Get(ctx context.Context, uid string) ([]byte, string, error) {
	code, etag, body, err := c.do(ctx, http.MethodGet, uid, nil, nil)
	if err != nil {
		return nil, "", err
	}
	switch code {
	case http.StatusOK:
		return body, etag, nil
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("there is no event with uid %q on this calendar", uid)
	default:
		return nil, "", fmt.Errorf("the calendar server answered HTTP %d to the read", code)
	}
}

// Delete removes one event, guarded by its ETag when known.
func (c *DAVClient) Delete(ctx context.Context, uid, etag string) error {
	h := map[string]string{}
	if etag != "" {
		h["If-Match"] = etag
	}
	code, _, _, err := c.do(ctx, http.MethodDelete, uid, nil, h)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	case http.StatusPreconditionFailed:
		return fmt.Errorf("the event changed on the server since it was read: read it again and retry")
	default:
		return fmt.Errorf("the calendar server answered HTTP %d to the delete", code)
	}
}

// newUID makes an event uid unique to this Talaria.
func newUID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@talaria"
}

// escapeText writes a TEXT value's escapes, the reverse of unescapeText.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// formatEvent renders one event as a VCALENDAR holding a single VEVENT.
// Timed events are written in UTC; all-day events as dates.
func formatEvent(e rawEvent, now time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Talaria//Calendar//EN\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\nDTSTAMP:%s\r\n", e.uid, now.UTC().Format("20060102T150405Z"))
	if e.allDay {
		fmt.Fprintf(&b, "DTSTART;VALUE=DATE:%s\r\n", e.start.Format("20060102"))
		end := e.end
		if !e.hasEnd || end.IsZero() {
			end = e.start.AddDate(0, 0, 1)
		}
		fmt.Fprintf(&b, "DTEND;VALUE=DATE:%s\r\n", end.Format("20060102"))
	} else {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", e.start.UTC().Format("20060102T150405Z"))
		end := e.end
		if !e.hasEnd || end.IsZero() {
			end = e.start.Add(time.Hour)
		}
		fmt.Fprintf(&b, "DTEND:%s\r\n", end.UTC().Format("20060102T150405Z"))
	}
	if e.rruleText != "" {
		fmt.Fprintf(&b, "RRULE:%s\r\n", e.rruleText)
	}
	for _, x := range e.exdates {
		if e.allDay {
			fmt.Fprintf(&b, "EXDATE;VALUE=DATE:%s\r\n", x.Format("20060102"))
		} else {
			fmt.Fprintf(&b, "EXDATE:%s\r\n", x.UTC().Format("20060102T150405Z"))
		}
	}
	fmt.Fprintf(&b, "SUMMARY:%s\r\n", escapeText(e.summary))
	if e.location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", escapeText(e.location))
	}
	if e.description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", escapeText(e.description))
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}
