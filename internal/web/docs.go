package web

import (
	"context"
	"net/http"
	"slices"

	"talaria/internal/docsearch"
)

type docsView struct {
	Enabled bool             `json:"enabled"`
	Folders []string         `json:"folders"`
	Status  docsearch.Status `json:"status"`
}

func (s *Server) docsView() docsView {
	v := docsView{Folders: []string{}}
	if s.Docs == nil {
		return v
	}
	v.Enabled = true
	if s.DocFolders != nil {
		v.Folders = s.DocFolders.Get()
	}
	v.Status = s.Docs.Status()
	return v
}

func (s *Server) docsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.docsView())
}

// docsPost changes the folders the agent may search (full paths of folders on this machine), or asks for a fresh look
// at everything now. A folder is only ever read, never changed, and places that hold secrets are refused.
func (s *Server) docsPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Folders *[]string `json:"folders"`
		Reindex bool      `json:"reindex"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Docs == nil || s.DocFolders == nil || s.Cfg == nil {
		fail(w, http.StatusNotFound, "search over your files is not set up here")
		return
	}
	if in.Folders != nil {
		var clean []string
		for _, p := range *in.Folders {
			c, err := docsearch.Check(p)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			if !slices.Contains(clean, c) {
				clean = append(clean, c)
			}
		}
		s.Cfg.DocSearch.Folders = clean
		if !s.writeConfig(w) {
			return
		}
		s.DocFolders.Set(clean)
		in.Reindex = true
	}
	if in.Reindex {
		go s.Docs.Refresh(context.Background())
	}
	writeJSON(w, http.StatusOK, s.docsView())
}
