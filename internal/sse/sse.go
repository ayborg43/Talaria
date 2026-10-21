// Package sse reads server-sent-event streams.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Stop, returned from a Read callback, ends reading without an error.
var Stop = errors.New("stop reading stream")

// Read parses a server-sent-events body and calls fn with the data payload
// of each event. Comments and non-data fields (event:, id:, retry:) are
// ignored: both providers put the event type inside the JSON.
func Read(r io.Reader, fn func(data string) error) error {
	br := bufio.NewReader(r) // not Scanner: events can exceed its token limit
	var data []string
	dispatch := func() (stop bool, err error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		if err := fn(payload); err != nil {
			if errors.Is(err, Stop) {
				return true, nil
			}
			return true, err
		}
		return false, nil
	}

	for {
		line, readErr := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if stop, err := dispatch(); stop || err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
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
