// Package guard is the permission layer around the agent's tools: rules about
// which tools run, grants the user makes with "always allow", a log of every
// call, and scrubbing of secrets from what the model sees and what is stored.
package guard

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask replaces a secret wherever it is found.
const Mask = "[redacted]"

// minSecret is the shortest value treated as a secret. Shorter ones would
// match ordinary text and mangle it.
const minSecret = 8

// Redactor removes secrets from text. It knows the specific values it was given
// (and their JSON- and URL-escaped forms), and the shapes of well-known token
// formats, so a key it was not told about is still caught if it looks like one.
type Redactor struct {
	mu       sync.RWMutex
	forms    map[string]bool
	literals *strings.Replacer
	patterns []*regexp.Regexp
}

var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}`),                                             // OpenAI, Anthropic
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),                                         // GitHub
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`),                                       // GitHub fine-grained
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),                                      // Slack
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                 // AWS access key ID
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),                                           // Google API key
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`),   // JWT
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_\-]{23,25}\.[A-Za-z0-9_\-]{6}\.[A-Za-z0-9_\-]{27,}`), // Discord bot token
	regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_\-]{35}\b`),                                      // Telegram bot token
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-]{20,}=*`),                             // an Authorization header
}

// NewRedactor makes a Redactor for the given secret values. Blank and very
// short values are ignored.
func NewRedactor(secrets []string) *Redactor {
	r := &Redactor{forms: map[string]bool{}, patterns: tokenPatterns}
	r.Add(secrets...)
	return r
}

// Add teaches the Redactor more secrets, such as a credential saved while
// Talaria is running. It is safe to call while others use Scrub.
func (r *Redactor) Add(secrets ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	add := func(s string) {
		if len(s) >= minSecret {
			r.forms[s] = true
		}
	}
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < minSecret {
			continue
		}
		add(s)
		if j, err := json.Marshal(s); err == nil { // how it looks inside a JSON string
			add(string(j[1 : len(j)-1]))
		}
		add(url.QueryEscape(s))
		add(url.PathEscape(s))
	}
	// Longest first, so a secret that contains another is hidden whole.
	forms := make([]string, 0, len(r.forms))
	for f := range r.forms {
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, Mask)
	}
	r.literals = nil
	if len(pairs) > 0 {
		r.literals = strings.NewReplacer(pairs...)
	}
}

// Scrub returns s with secrets masked.
func (r *Redactor) Scrub(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	lit, pats := r.literals, r.patterns
	r.mu.RUnlock()
	if lit != nil {
		s = lit.Replace(s)
	}
	for _, p := range pats {
		s = p.ReplaceAllString(s, Mask)
	}
	return s
}

var secretName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIAL)S?$`)

// EnvSecrets picks out the values of environment variables whose names say they
// hold secrets (API_KEY, ..._TOKEN, ..._PASSWORD and so on). environ is in the
// form os.Environ returns.
func EnvSecrets(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok && secretName.MatchString(name) && len(value) >= minSecret {
			out = append(out, value)
		}
	}
	return out
}
