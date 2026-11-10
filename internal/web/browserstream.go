package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// maxStreams bounds how many live views of the agent's browser run at once: each takes a picture several times a
// second, so a forgotten tab must not pile them up. A fourth is refused and the page falls back to polling.
const maxStreams = 3

var streams atomic.Int32

// streamEvery is how often a live view looks at the page; a picture that has not changed is not sent again.
const streamEvery = 350 * time.Millisecond

// browserStream is a live view of a tab: a multipart stream of JPEGs that an <img> shows as it arrives, so the page
// needs no code to draw it and nothing is kept: each picture is sent and dropped. It runs only while the page has
// it open, ends when the page goes away or the tab closes, and sends a picture only when the page has changed.
func (s *Server) browserStream(w http.ResponseWriter, r *http.Request) {
	_, p := s.tab(r.URL.Query().Get("id"))
	if p == nil {
		fail(w, http.StatusNotFound, "that tab is no longer open")
		return
	}
	if streams.Add(1) > maxStreams {
		streams.Add(-1)
		fail(w, http.StatusTooManyRequests, "too many live views are open")
		return
	}
	defer streams.Add(-1)
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "this connection cannot stream")
		return
	}
	const boundary = "talariaframe"
	h := w.Header()
	h.Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // a proxy must pass each picture on as it comes
	w.WriteHeader(http.StatusOK)
	if err := openStream(w, boundary); err != nil {
		return
	}
	fl.Flush()

	var last []byte
	tick := time.NewTicker(streamEvery)
	defer tick.Stop()
	for {
		img, err := p.Shot(r.Context(), 50)
		if err != nil {
			return // the tab closed, or the page went away
		}
		if !bytes.Equal(img, last) {
			last = img
			if err := writeFrame(w, boundary, img); err != nil {
				return
			}
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// A browser draws a picture of a multipart stream only once the next part's header has arrived, and on a page that
// has stopped changing there is no next picture to wait for. So the header of the part to come is always written
// straight after a picture, and the picture before it is complete. No length is given: the browser reads each part
// up to the next boundary.
const partHeader = "Content-Type: image/jpeg\r\n\r\n"

// openStream starts a stream: the first boundary and the header of the first picture.
func openStream(w io.Writer, boundary string) error {
	_, err := fmt.Fprintf(w, "--%s\r\n%s", boundary, partHeader)
	return err
}

// writeFrame sends one picture, ends its part, and opens the next.
func writeFrame(w io.Writer, boundary string, img []byte) error {
	if _, err := w.Write(img); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\r\n--%s\r\n%s", boundary, partHeader)
	return err
}
