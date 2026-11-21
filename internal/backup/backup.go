// Package backup saves Talaria's state directory to one archive and puts it back.
//
// What is kept: the config, the encrypted vault, notes, skills, history, jobs,
// goals, watches, grants and the activity log. What is not: the browser's
// profile (large, and full of cookies), temporary files, and the vault's key
// file, which belongs somewhere other than next to the vault it unlocks.
// Kept downloads are left out too: they expire on their own, so archiving
// them would only bloat the backup with files about to be deleted.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"talaria/internal/memory"
)

const (
	manifestName = "BACKUP.json"
	historyName  = "sessions.db"
	// maxRestoreBytes bounds what a restore will write, so a hostile or broken archive cannot fill the disk.
	maxRestoreBytes = 8 << 30
)

// Manifest describes an archive.
type Manifest struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	Files   int       `json:"files"`
}

// Options adjusts Create.
type Options struct {
	// Exclude lists absolute paths to leave out, such as the vault's key file.
	Exclude []string
	Now     func() time.Time
}

// Report says what Create did.
type Report struct {
	Files   int
	Bytes   int64
	Skipped []string
}

func skipped(rel string) bool {
	switch {
	case rel == "browser" || strings.HasPrefix(rel, "browser/"):
		return true
	case rel == "files" || strings.HasPrefix(rel, "files/"):
		return true // expiring downloads are cache, not state
	case rel == PendingName || strings.HasPrefix(rel, ".before-restore-") || strings.HasPrefix(rel, ".restore-staging"):
		return true // a restore's own working files: archiving them would bury the last backup inside the next
	case strings.HasSuffix(rel, ".tmp"), strings.HasSuffix(rel, ".lock"), strings.HasSuffix(rel, "-wal"), strings.HasSuffix(rel, "-shm"), strings.HasSuffix(rel, "-journal"):
		return true // the history is saved as a snapshot instead
	}
	return false
}

// Create writes home as a gzip-compressed tar archive to w.
func Create(home string, w io.Writer, o Options) (Report, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	info, err := os.Stat(home)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("%s is not a directory", home)
	}
	excluded := map[string]bool{}
	for _, e := range o.Exclude {
		if abs, err := filepath.Abs(e); err == nil {
			excluded[abs] = true
		}
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var rep Report
	var names []string

	add := func(rel, src string, mode fs.FileMode, size int64) error {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: int64(mode.Perm()), Size: size, ModTime: now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		n, err := io.Copy(tw, f)
		rep.Bytes += n
		rep.Files++
		names = append(names, rel)
		return err
	}

	walkErr := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		abs, _ := filepath.Abs(p)
		switch {
		case excluded[abs]:
			rep.Skipped = append(rep.Skipped, rel+" (kept out on purpose)")
			return skipDir(d)
		case skipped(rel):
			if !strings.HasSuffix(rel, "-wal") && !strings.HasSuffix(rel, "-shm") && !strings.HasSuffix(rel, "-journal") {
				rep.Skipped = append(rep.Skipped, rel)
			}
			return skipDir(d)
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Name: rel + "/", Mode: 0o700, ModTime: now(), Typeflag: tar.TypeDir})
		case !d.Type().IsRegular():
			rep.Skipped = append(rep.Skipped, rel+" (not a regular file)")
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if rel == historyName {
			return addHistory(p, add)
		}
		return add(rel, p, fi.Mode(), fi.Size())
	})
	if walkErr != nil {
		return rep, walkErr
	}

	manifest, _ := json.MarshalIndent(Manifest{Format: 1, Created: now(), Files: rep.Files}, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifest)), ModTime: now(), Typeflag: tar.TypeReg}); err != nil {
		return rep, err
	}
	if _, err := tw.Write(manifest); err != nil {
		return rep, err
	}
	if err := tw.Close(); err != nil {
		return rep, err
	}
	return rep, gz.Close()
}

func skipDir(d fs.DirEntry) error {
	if d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// addHistory saves the history database as a consistent snapshot, since it may be in use.
func addHistory(src string, add func(rel, src string, mode fs.FileMode, size int64) error) error {
	dir, err := os.MkdirTemp("", "talaria-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snap := filepath.Join(dir, historyName)
	if err := memory.Snapshot(src, snap); err != nil {
		return fmt.Errorf("saving the history: %w", err)
	}
	fi, err := os.Stat(snap)
	if err != nil {
		return err
	}
	return add(historyName, snap, 0o600, fi.Size())
}

// Restore unpacks an archive made by Create into home. If home already holds
// anything, the archive is refused unless replace is set, in which case what was
// there is moved aside to a sibling directory (named in the result), not deleted.
// Everything lands owner-only. The archive is checked before anything changes:
// only plain files and directories, only names inside home.
func Restore(archive io.Reader, home string, replace bool, now func() time.Time) (movedAside string, files int, err error) {
	if now == nil {
		now = time.Now
	}
	home = filepath.Clean(home)
	parent := filepath.Dir(home)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", 0, err
	}
	staging, err := os.MkdirTemp(parent, ".talaria-restore-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(staging) // a failed restore leaves nothing behind; a good one has renamed it away

	files, err = unpack(archive, staging)
	if err != nil {
		return "", 0, err
	}

	if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
		if !replace {
			return "", 0, fmt.Errorf("%s already holds data: stop Talaria, then use -replace to move it aside and restore", home)
		}
		movedAside = home + ".before-restore-" + now().Format("20060102-150405")
		if err := os.Rename(home, movedAside); err != nil {
			return "", 0, fmt.Errorf("moving the current state aside: %w", err)
		}
	} else if err == nil {
		os.Remove(home) // an empty directory: make way
	}
	if err := os.Rename(staging, home); err != nil {
		if movedAside != "" {
			os.Rename(movedAside, home) // put it back
		}
		return "", 0, err
	}
	os.Chmod(home, 0o700)
	return movedAside, files, nil
}

func checkName(name string) error {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("unsafe name in the archive: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "" || part == "." {
			return fmt.Errorf("unsafe name in the archive: %q", name)
		}
	}
	return nil
}

// PendingName is the file a restore uploaded from the app waits in, inside home, until Talaria next starts.
const PendingName = "pending-restore.tar.gz"

// Verify reads an archive from start to end without writing any of it, and says what it holds: it must be a Talaria
// backup, hold only plain files and folders with safe names, and be no bigger than a restore will write.
func Verify(archive io.Reader) (files int, created time.Time, err error) {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return 0, time.Time{}, errors.New("this is not a Talaria backup (not a gzip file)")
	}
	tr := tar.NewReader(gz)
	var total int64
	saw := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, time.Time{}, fmt.Errorf("reading the archive: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == manifestName {
			var m Manifest
			if json.NewDecoder(io.LimitReader(tr, 1<<16)).Decode(&m) == nil {
				created = m.Created
			}
			saw = true
			continue
		}
		if err := checkName(name); err != nil {
			return 0, time.Time{}, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > maxRestoreBytes {
				return 0, time.Time{}, errors.New("the archive is larger than a restore will write")
			}
			if n, err := io.Copy(io.Discard, tr); err != nil || n != h.Size {
				return 0, time.Time{}, fmt.Errorf("the archive is cut short at %s", name)
			}
			files++
		default:
			return 0, time.Time{}, fmt.Errorf("%s is not a plain file or directory (type %q): refusing the archive", name, string(h.Typeflag))
		}
	}
	if !saw {
		return 0, time.Time{}, errors.New("this is not a Talaria backup (no " + manifestName + ")")
	}
	return files, created, nil
}

// RestoreInPlace puts an archive into home without renaming home itself, which cannot be done when home is a mounted
// volume (a Docker /data). The archive is unpacked and checked beside the current state first; then each current entry is
// moved into a ".before-restore-DATE" folder inside home (nothing is deleted) and the unpacked entries take its place.
// keep names entries that stay where they are (the file the archive was uploaded as, say). It reports the folder the old state
// went to, which is empty when home held nothing.
func RestoreInPlace(archive io.Reader, home string, keep []string, now func() time.Time) (movedAside string, files int, err error) {
	if now == nil {
		now = time.Now
	}
	home = filepath.Clean(home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", 0, err
	}
	staging := filepath.Join(home, ".restore-staging")
	os.RemoveAll(staging) // left by an attempt that was cut off
	if err := os.Mkdir(staging, 0o700); err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(staging)
	if files, err = unpack(archive, staging); err != nil {
		return "", 0, err
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return "", 0, err
	}
	var old []string
	for _, e := range entries {
		n := e.Name()
		if n == ".restore-staging" || slices.Contains(keep, n) || strings.HasPrefix(n, ".before-restore-") {
			continue
		}
		old = append(old, n)
	}
	if len(old) > 0 {
		movedAside = filepath.Join(home, ".before-restore-"+now().Format("20060102-150405"))
		if err := os.Mkdir(movedAside, 0o700); err != nil {
			return "", 0, err
		}
		for i, n := range old {
			if err := os.Rename(filepath.Join(home, n), filepath.Join(movedAside, n)); err != nil {
				for _, back := range old[:i] { // put back what was moved: no half-restored state
					os.Rename(filepath.Join(movedAside, back), filepath.Join(home, back))
				}
				os.Remove(movedAside)
				return "", 0, fmt.Errorf("moving %s aside: %w", n, err)
			}
		}
	}
	news, err := os.ReadDir(staging)
	if err != nil {
		return movedAside, 0, err
	}
	for _, e := range news {
		if err := os.Rename(filepath.Join(staging, e.Name()), filepath.Join(home, e.Name())); err != nil {
			return movedAside, files, fmt.Errorf("putting %s in place (the old state is in %s): %w", e.Name(), movedAside, err)
		}
	}
	return movedAside, files, nil
}

// unpack writes an archive into dir after checking each entry: only plain files and folders, only names inside dir.
func unpack(archive io.Reader, dir string) (files int, err error) {
	staging := dir
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return 0, errors.New("this is not a Talaria backup (not a gzip file)")
	}
	tr := tar.NewReader(gz)
	var total int64
	sawManifest := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("reading the archive: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == manifestName {
			sawManifest = true
			continue
		}
		if err := checkName(name); err != nil {
			return 0, err
		}
		dst := filepath.Join(staging, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return 0, err
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > maxRestoreBytes {
				return 0, errors.New("the archive is larger than a restore will write")
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return 0, err
			}
			f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return 0, fmt.Errorf("%s appears twice or cannot be written: %w", name, err)
			}
			n, err := io.Copy(f, io.LimitReader(tr, h.Size))
			f.Close()
			if err != nil || n != h.Size {
				return 0, fmt.Errorf("the archive is cut short at %s", name)
			}
			files++
		default:
			return 0, fmt.Errorf("%s is not a plain file or directory (type %q): refusing the archive", name, string(h.Typeflag))
		}
	}

	if !sawManifest {
		return 0, errors.New("this is not a Talaria backup (no " + manifestName + ")")
	}
	return files, nil
}
