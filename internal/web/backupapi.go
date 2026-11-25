package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// Notes says what the waiting backup's settings would change that is more than data: programs it starts, rules it sets.
	Notes []string `json:"notes,omitempty"`
}

// restoreCache remembers what the waiting backup was found to hold, by size and time, so looking at the page does not read the whole file again.
type restoreCache struct {
	mu   sync.Mutex
	size int64
	mod  time.Time
	view restoreView
	ok   bool
}

func (s *Server) restoreView() restoreView {
	v := restoreView{Available: s.backupOn()}
	if !v.Available {
		return v
	}
	fi, err := os.Stat(s.pendingPath())
	if err != nil {
		return v
	}
	c := &s.restoreSeen
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.view
	}
	f, err := os.Open(s.pendingPath())
	if err != nil {
		return v
	}
	defer f.Close()
	if n, created, err := backup.Verify(f); err == nil {
		v.Pending, v.Files, v.Created = true, n, created
		if _, err := f.Seek(0, 0); err == nil {
			v.Notes = backup.ConfigNotes(f)
		}
	}
	c.size, c.mod, c.view, c.ok = fi.Size(), fi.ModTime(), v, true
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
	if !s.restoreBusy.TryLock() { // one upload at a time: two would only fight over the disk
		fail(w, http.StatusConflict, "another backup is being uploaded: wait for it to finish")
		return
	}
	defer s.restoreBusy.Unlock()
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	f, err := os.CreateTemp(s.Home, "pending-restore-*.part") // its own name, 0600, so nothing else can be mistaken for it
	if err != nil {
		fail(w, http.StatusInternalServerError, "the backup could not be saved here")
		return
	}
	tmp := f.Name()
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
