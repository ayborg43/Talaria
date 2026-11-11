package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Taking over a page must be usable for the thing it is most often for: a
// sign-in. The panel offers real username/password boxes (filled into the
// page, never kept), typing cannot be overtaken by the next tap, Tab updates
// the masked box, and a keyboard can type straight at the picture.
func TestTheBrowserPanelMakesSigningInEasy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("static", "more.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		"kind: 'login'", "autocomplete: 'username'", "autocomplete: 'current-password'",
		"Fill sign-in fields", "loginForm.hidden = !t.login",
		"const flushNow", "await flushNow();", // pending typing reaches the page before any tap
		"a.kind === 'key'", // Tab moving focus updates the typing box (masked for a password)
		"stage.addEventListener('keydown'", "stage.addEventListener('paste'",
		"addEventListener('pointerup'", // a swipe scrolls instead of clicking what it lands on
		"userIn.value = ''; passIn.value = '';",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("more.js lost %q", want)
		}
	}
	c, err := os.ReadFile(filepath.Join("static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".login-form", ".toolbar.typing{position:sticky"} {
		if !strings.Contains(string(c), want) {
			t.Errorf("app.css lost %q", want)
		}
	}
}
