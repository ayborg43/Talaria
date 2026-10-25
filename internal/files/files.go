// Package files keeps what the user uploads from the web app and what the
// agent makes for them: PDFs and images, served back as downloads. Every file
// has a lifetime: Purge removes what is older than the retention the admin
// sets (12 hours by default), so downloads never pile up on the server.
package files

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Limits keep uploads bounded.
const (
	// MaxBytes is the largest single file: 10 MB.
	MaxBytes = 10 << 20
	// DefaultRetentionHours is how long downloads live when the admin sets nothing.
	DefaultRetentionHours = 12
	// MinRetentionHours and MaxRetentionHours bound the admin's setting.
	MinRetentionHours = 1
	MaxRetentionHours = 168
)

// Origins say where a file came from.
const (
	OriginUpload    = "upload"
	OriginGenerated = "generated"
)

// Kinds the store accepts, detected from the bytes, never the name.
var kinds = map[string]string{
	"application/pdf": ".pdf",
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
}

// ClampRetention keeps the admin's hours inside 1..168, defaulting to 12.
func ClampRetention(hours int) int {
	if hours <= 0 {
		return DefaultRetentionHours
	}
	if hours < MinRetentionHours {
		return MinRetentionHours
	}
	if hours > MaxRetentionHours {
		return MaxRetentionHours
	}
	return hours
}

// File is one stored file.
type File struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"` // what the user called it
	MIME    string    `json:"mime"`
	Size    int64     `json:"size"`
	Origin  string    `json:"origin"` // upload or generated
	Created time.Time `json:"created"`
}

// Store keeps files in one directory with an index, reread on every call.
// Everything is safe for use from many goroutines.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) indexPath() string { return filepath.Join(s.Dir, "index.json") }

func (s *Store) load() ([]File, error) {
	raw, err := os.ReadFile(s.indexPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Files []File `json:"files"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.indexPath(), err)
	}
	return f.Files, nil
}

func (s *Store) save(files []File) error {
	raw, err := json.MarshalIndent(struct {
		Files []File `json:"files"`
	}{files}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "index-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.indexPath())
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// safeName keeps the user's name without letting it reach the filesystem:
// the stored file is always <id><ext>.
func safeName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, ".")
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if name == "" {
		name = "file"
	}
	return name
}

// Detect returns the MIME of data from its magic bytes, or "" when it is not
// a PDF or a supported image. The name is never trusted. Go's own sniffer
// does not know PDFs, so their %PDF- prefix is checked first.
func Detect(data []byte) string {
	if len(data) > 5 && string(data[:5]) == "%PDF-" {
		return "application/pdf"
	}
	mime := http.DetectContentType(data)
	if _, ok := kinds[mime]; ok {
		return mime
	}
	return ""
}

// Save stores data under a random id and returns its record. Data that is
// not a PDF or a supported image is refused.
func (s *Store) Save(name string, data []byte, origin string) (File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) == 0 {
		return File{}, errors.New("the file is empty")
	}
	if len(data) > MaxBytes {
		return File{}, fmt.Errorf("the file is too big (%d MB at most)", MaxBytes>>20)
	}
	mime := Detect(data)
	if mime == "" {
		return File{}, errors.New("only PDF files and PNG, JPEG, GIF or WebP images can be kept")
	}
	if origin != OriginGenerated {
		origin = OriginUpload
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return File{}, err
	}
	name = safeName(name)
	if !strings.HasSuffix(strings.ToLower(name), kinds[mime]) {
		name += kinds[mime]
	}
	id := newID()
	path := filepath.Join(s.Dir, id+kinds[mime])
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return File{}, err
	}
	f := File{ID: id, Name: name, MIME: mime, Size: int64(len(data)), Origin: origin, Created: s.now()}
	files, err := s.load()
	if err != nil {
		os.Remove(path)
		return File{}, err
	}
	files = append(files, f)
	if err := s.save(files); err != nil {
		os.Remove(path)
		return File{}, err
	}
	return f, nil
}

// MakePDF builds a PDF document from a title and text and stores it,
// returning its record for the download link.
func (s *Store) MakePDF(title, text string) (File, error) {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return File{}, errors.New("a PDF needs a title")
	}
	if strings.TrimSpace(text) == "" {
		return File{}, errors.New("a PDF needs some text")
	}
	return s.Save(title+".pdf", GeneratePDF(title, text), OriginGenerated)
}

// Open reads one file's bytes.
func (s *Store) Open(id string) (File, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return File{}, nil, err
	}
	for _, f := range files {
		if f.ID != id {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Dir, f.ID+kinds[f.MIME]))
		if err != nil {
			return File{}, nil, errors.New("no such file")
		}
		return f, raw, nil
	}
	return File{}, nil, errors.New("no such file")
}

// List returns every stored file, newest first.
func (s *Store) List() ([]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]File(nil), files...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Delete removes one file's record and bytes.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return err
	}
	kept := files[:0]
	found := false
	for _, f := range files {
		if f.ID == id {
			found = true
			os.Remove(filepath.Join(s.Dir, f.ID+kinds[f.MIME]))
			continue
		}
		kept = append(kept, f)
	}
	if !found {
		return errors.New("no such file")
	}
	return s.save(kept)
}

// Purge deletes every file older than maxAge and returns how many went.
// Run it at startup, on a timer, and after every upload and download: the
// index is small, so sweeping is cheap.
func (s *Store) Purge(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.load()
	if err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-maxAge)
	kept := files[:0]
	gone := 0
	for _, f := range files {
		if f.Created.After(cutoff) {
			kept = append(kept, f)
			continue
		}
		os.Remove(filepath.Join(s.Dir, f.ID+kinds[f.MIME]))
		gone++
	}
	if gone == 0 {
		return 0, nil
	}
	return gone, s.save(kept)
}
