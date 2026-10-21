package mcp

import (
	"io"
	"net/http"
)

// openAndFollow plays the user's browser: it visits the sign-in address and
// follows the redirect back to Talaria's own callback.
func openAndFollow(addr string) error {
	resp, err := http.Get(addr)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
