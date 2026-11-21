package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"talaria/internal/backup"
)

// maxRestoreUpload bounds an uploaded backup. A real one is mostly history and notes; browser profiles and downloads
// are left out of it.
const maxRestoreUpload = 4 << 30

func (s *Server) backupOn() bool { return s.Home != "" }

func (s *Server) pendingPath() string { return filepath.Join(s.Home, backup.PendingName) }

// backupKeyFiles are the vault's key files, which a backup must not carry.
func (s *Server) backupKeyFiles() []string {
	ex := []string{filepath.Join(s.Home, "vault.key")}
	if s.Cfg != nil && s.Cfg.Vault.KeyFile != "" {
		k := s.Cfg.Vault.KeyFile
		if strings.HasPrefix(k, "~/") {
			if h, err := os.UserHomeDir(); err == nil {
				k = filepath.Join(h, k[2:])
			}
		}
		ex = append(ex, k)
	}
	return ex
}

// backupGet is the whole state of Talaria as one archive, to keep or to put on another server. It holds the history,
// notes, skills, jobs, goals and config, and the vault in its encrypted form, but not the vault's key file.
func (s *Server) backupGet(w http.ResponseWriter, r *http.Request) {
	if !s.backupOn() {
		fail(w, http.StatusNotImplemented, "backups are not set up here")
		return
	}
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="talaria-backup-`+time.Now().Format("20060102-150405")+`.tar.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := backup.Create(s.Home, w, backup.Options{Exclude: s.backupKeyFiles()}); err != nil {
		s.logf("web: backup: %v", err) // the download is already under way: all that is left is to cut it short
	}
}

type restoreView struct {
	Available bool      `json:"available"`
	Pending   bool      `json:"pending"`
	Created   time.Time `json:"created,omitzero"`
	Files     int       `json:"files,omitempty"`
}

func (s *Server) restoreView() restoreView {
	v := restoreView{Available: s.backupOn()}
	if !v.Available {
		return v
	}
	f, err := os.Open(s.pendingPath())
	if err != nil {
		return v
	}
	defer f.Close()
	if n, created, err := backup.Verify(f); err == nil {
		v.Pending, v.Files, v.Created = true, n, created
	}
	return v
}

func (s *Server) restoreGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.restoreView())
}

// restorePost takes a backup file as the request body, checks all of it, and keeps it to be put in place the next time
// Talaria starts: the state it replaces is in use now (open databases, running jobs), so nothing is changed until then.
// The state it replaces is moved aside, not deleted. ?cancel=1 forgets a waiting backup.
func (s *Server) restorePost(w http.ResponseWriter, r *http.Request) {
	if !s.backupOn() {
		fail(w, http.StatusNotImplemented, "restoring is not set up here")
		return
	}
	if r.URL.Query().Get("cancel") != "" {
		os.Remove(s.pendingPath())
		writeJSON(w, http.StatusOK, s.restoreView())
		return
	}
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	tmp := s.pendingPath() + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fail(w, http.StatusInternalServerError, "the backup could not be saved here")
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxRestoreUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		fail(w, http.StatusBadRequest, "the backup did not arrive whole (or is too large)")
		return
	}
	check, err := os.Open(tmp)
	if err == nil {
		_, _, err = backup.Verify(check)
		check.Close()
	}
	if err != nil {
		os.Remove(tmp)
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Rename(tmp, s.pendingPath()); err != nil {
		os.Remove(tmp)
		fail(w, http.StatusInternalServerError, "the backup could not be saved here")
		return
	}
	s.logf("web: a backup was uploaded and will be restored at the next start")
	v := s.restoreView()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restore": v, "restart_needed": true})
}
