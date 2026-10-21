package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newServerNoAuth is an MCP server that wants no sign-in.
func newServerNoAuth(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp"
}
