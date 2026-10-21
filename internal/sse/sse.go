// Package sse reads server-sent-event streams.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const (
	maxLine  = 8 << 20  // the longest single line accepted
	maxEvent = 16 << 20 // the most data one event may gather
)

// ErrTooLong means a line or event went past the limits above.
var ErrTooLong = errors.New("event stream line or event is too long")

// Stop, returned from a Read callback, ends reading without an error.
var Stop = errors.New("stop reading stream")

// Read parses a server-sent-events body and calls fn with the data payload
// of each event. Comments and non-data fields (event:, id:, retry:) are
// ignored: both providers put the event type inside the JSON.
func Read(r io.Reader, fn func(data string) error) error {
	br := bufio.NewReader(r) // not Scanner: events can exceed its token limit
	var data []string
	size := 0
	dispatch := func() (stop bool, err error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		size = 0
		if err := fn(payload); err != nil {
			if errors.Is(err, Stop) {
				return true, nil
			}
			return true, err
		}
		return false, nil
	}

	for {
		line, readErr := readLine(br)
		if errors.Is(readErr, ErrTooLong) {
			return readErr
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if stop, err := dispatch(); stop || err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			if size += len(line); size > maxEvent {
				return ErrTooLong
			}
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if readErr != nil {
			if readErr == io.EOF { // a final event may lack its blank line
				_, err := dispatch()
				return err
			}
			return readErr
		}
	}
}

// readLine reads up to and including a newline, failing with ErrTooLong rather than
// buffering a line without end.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		part, err := br.ReadSlice('\n')
		sb.Write(part)
		if sb.Len() > maxLine {
			return "", ErrTooLong
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return sb.String(), err
	}
}
