package web

import (
	"strings"
	"testing"
)

// The Android build ships inside the binary: a phone can download it from the page it signs in on, without a session.
func TestAndroidAppIsServed(t *testing.T) {
	r := newRig(t)
	resp, body := r.do("GET", "/talaria.apk", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the APK answered %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.android.package-archive" {
		t.Fatalf("the APK was served as %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "Talaria.apk") {
		t.Fatalf("the APK was served with %q", cd)
	}
	intent, _ := staticFS.ReadFile("static/talaria.apk")
	if len(body) != len(intent) || len(body) < 2 || body[0] != 'P' || body[1] != 'K' {
		t.Fatalf("the APK did not come back whole (%d of %d bytes)", len(body), len(intent))
	}
}
