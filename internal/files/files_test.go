package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngProbe = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
var pdfProbe = []byte("%PDF-1.4 fake body for the detector....................")

func testStore(t *testing.T) *Store {
	t.Helper()
	now := time.Now()
	return &Store{Dir: filepath.Join(t.TempDir(), "files"), Now: func() time.Time { return now }}
}

func TestSaveDetectsKindFromBytes(t *testing.T) {
	s := testStore(t)
	f, err := s.Save("../../etc/passwd", append(pngProbe, make([]byte, 100)...), OriginUpload)
	if err != nil {
		t.Fatal(err)
	}
	if f.MIME != "image/png" || !strings.HasSuffix(f.Name, ".png") {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(f.ID, "") || len(f.ID) != 32 {
		t.Fatalf("ids are random hex: %q", f.ID)
	}
	// The stored bytes are under the id, never the user's name.
	if _, err := os.Stat(filepath.Join(s.Dir, "../../etc/passwd")); !os.IsNotExist(err) {
		t.Fatal("the user's name reached the filesystem")
	}
}

func TestSaveRefusesWhatItShouldNot(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save("x.html", []byte("<html><body>hi.....................</body></html>......"), OriginUpload); err == nil {
		t.Fatal("HTML was kept")
	}
	if _, err := s.Save("empty.pdf", pdfProbe[:0], OriginUpload); err == nil {
		t.Fatal("an empty file was kept")
	}
	big := append(pdfProbe, make([]byte, MaxBytes)...)
	if _, err := s.Save("big.pdf", big, OriginUpload); err == nil {
		t.Fatal("an oversize file was kept")
	}
}

func TestPDFRoundTrip(t *testing.T) {
	s := testStore(t)
	raw := GeneratePDF("Shopping", "eggs\nmilk (2l)\ncaf\u00e9 \u2615")
	if !strings.HasPrefix(string(raw), "%PDF-1.4") || !strings.Contains(string(raw), "%%EOF") {
		t.Fatal("not a PDF")
	}
	if !strings.Contains(string(raw), "(Shopping)") || !strings.Contains(string(raw), "caf\xe9 ?") {
		t.Fatalf("title or folded text missing:\n%s", raw[:400])
	}
	f, err := s.MakePDF("Shopping", "eggs\nmilk")
	if err != nil || f.MIME != "application/pdf" || f.Origin != OriginGenerated {
		t.Fatalf("%v %+v", err, f)
	}
	got, data, err := s.Open(f.ID)
	if err != nil || got.ID != f.ID || !strings.HasPrefix(string(data), "%PDF-1.4") {
		t.Fatalf("%v %+v", err, got)
	}
	if _, _, err := s.Open("nope"); err == nil {
		t.Fatal("a missing file opened")
	}
}

func TestPurgeKeepsTheFresh(t *testing.T) {
	now := time.Now()
	s := &Store{Dir: filepath.Join(t.TempDir(), "files"), Now: func() time.Time { return now }}
	old := &Store{Dir: s.Dir, Now: func() time.Time { return now.Add(-13 * time.Hour) }}
	f, err := old.Save("old.pdf", pdfProbe, OriginUpload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("new.pdf", pdfProbe, OriginUpload); err != nil {
		t.Fatal(err)
	}
	n, err := s.Purge(12 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("one file should go: %d %v", n, err)
	}
	if _, _, err := s.Open(f.ID); err == nil {
		t.Fatal("the old file still opens")
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if err := s.Delete(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestClampRetention(t *testing.T) {
	for in, want := range map[int]int{0: 12, -5: 12, 1: 1, 6: 6, 168: 168, 999: 168} {
		if got := ClampRetention(in); got != want {
			t.Fatalf("ClampRetention(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestQuotaEvictsOldestFirst(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	at := now
	s := &Store{Dir: dir, MaxTotalBytes: 200, Now: func() time.Time { return at }}
	ids := []string{}
	for _, name := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		f, err := s.Save(name, append(pdfProbe, make([]byte, 40)...), OriginUpload)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
		at = at.Add(time.Minute)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "c.pdf" || list[1].Name != "b.pdf" {
		t.Fatalf("oldest should go first: %+v", list)
	}
	if _, _, err := s.Open(ids[0]); err == nil {
		t.Fatal("the evicted file still opens")
	}
	if got := QuotaBytes(0); got != DefaultMaxTotalBytes {
		t.Fatalf("default quota: %d", got)
	}
}
