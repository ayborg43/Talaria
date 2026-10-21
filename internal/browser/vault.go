package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"talaria/internal/tool"
	"talaria/internal/vault"
)

// Credentials is the vault as the browser tools see it.
type Credentials interface {
	List() []vault.Info
	Get(name string) (vault.Credential, bool)
}

// RegisterVault adds the tools that use saved logins: vault_list, which names
// them, and browser_fill, which puts one into a field of the page.
//
// The model asks for a credential by name and never receives its value: not in
// a result, not in the page listing (a password field shows only that it is
// filled in), not in the activity log. The tool refuses to use a credential on
// any site it was not saved for, so a page made to look like a login cannot
// get one out of the agent however it words its request.
func RegisterVault(r *tool.Registry, m *Manager, owner string, creds Credentials) {
	t := &toolset{m: m, owner: owner, creds: creds}
	r.Register(tool.Tool{
		Name: "vault_list",
		Description: "List the logins and keys saved for you to use in the browser, by name, with the sites each may be used on. " +
			"You cannot read their values; browser_fill puts one into a field. The user adds credentials at the command line.",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run:    t.vaultList,
	})
	r.Register(tool.Tool{
		Untrusted: true,
		Name:      "browser_fill",
		Description: "Fill a saved credential into a field of the current page without seeing it: its username, its password, or (for a saved key) its value. " +
			"It works only on the sites the credential was saved for, and a password or value goes only into a password field. " +
			"To log in: fill the username field, fill the password field, then click the sign-in button. Returns the page afterwards.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"ref":{"type":"integer","description":"element number from the latest page listing"},
			"credential":{"type":"string","description":"the saved name, from vault_list"},
			"field":{"type":"string","enum":["username","password","value"],"description":"which part; for a login it is username or password, for a saved key it is value"}},
			"required":["ref","credential","field"]}`),
		NeedsApproval: true,
		Describe:      t.describeFill,
		Run:           t.fill,
	})
}

func (t *toolset) vaultList(ctx context.Context, _ json.RawMessage) (string, error) {
	var infos []vault.Info
	for _, i := range t.creds.List() {
		if i.Kind != vault.KindOAuth { // a sign-in to an MCP server is not something to type into a page
			infos = append(infos, i)
		}
	}
	if len(infos) == 0 {
		return "no credentials are saved (the user adds them with `tal vault add`)", nil
	}
	var b strings.Builder
	for _, i := range infos {
		fmt.Fprintf(&b, "%s | %s | for use on %s\n", i.Name, i.Kind, strings.Join(i.Sites, ", "))
	}
	return b.String(), nil
}

type fillArgs struct {
	Ref        int
	Credential string
	Field      string
}

func (t *toolset) fill(ctx context.Context, raw json.RawMessage) (string, error) {
	var a fillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	cred, ok := t.creds.Get(a.Credential)
	if !ok {
		names := make([]string, 0)
		for _, i := range t.creds.List() {
			names = append(names, i.Name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("no credential named %q (saved: %s)", a.Credential, orNone(names))
	}
	value, secret, err := part(cred, a.Field)
	if err != nil {
		return "", err
	}
	p, note, err := t.page(ctx)
	if err != nil {
		return "", err
	}
	if note != "" {
		return "", errors.New(note)
	}
	// Where the page really is now, asked of the browser, not what the model says.
	here, err := p.URL(ctx)
	if err != nil {
		return "", err
	}
	if err := cred.AllowsURL(here); err != nil {
		return "", err
	}
	el, _, known := p.Element(a.Ref)
	switch {
	case !known:
		return "", ErrStale
	case secret && el.Type != "password":
		return "", fmt.Errorf("%s is not a password field: a password goes only into one, because anything else shows what is typed (and the page is photographed for the model). If the site really has a plain field for it, the user must type it", Brief(el))
	}
	mustBe := ""
	if secret {
		mustBe = "password"
	}
	if err := p.TypeInto(ctx, a.Ref, value, true, false, mustBe); err != nil {
		return "", err
	}
	if secret {
		t.m.NoteSecret(t.owner, here)
	}
	what := a.Field + " of " + cred.Name
	return t.finish(ctx, p, fmt.Sprintf("Filled the %s into %s.", what, Brief(el)))
}

// part picks the field of a credential to fill, and says whether it is a secret.
func part(c vault.Credential, field string) (value string, secret bool, err error) {
	switch {
	case c.Kind == vault.KindLogin && field == "username":
		return c.Username, false, nil
	case c.Kind == vault.KindLogin && field == "password":
		return c.Secret, true, nil
	case c.Kind == vault.KindSecret && field == "value":
		return c.Secret, true, nil
	case c.Kind == vault.KindOAuth:
		return "", false, fmt.Errorf("%q is a sign-in to a connected service, which Talaria uses itself: it cannot be filled into a page", c.Name)
	case c.Kind == vault.KindLogin:
		return "", false, fmt.Errorf("a login has a username and a password: field must be one of those, not %q", field)
	}
	return "", false, fmt.Errorf("%q is a single saved value: field must be \"value\", not %q", c.Name, field)
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (t *toolset) describeFill(raw json.RawMessage) string {
	var a fillArgs
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	_, url := t.where()
	el, _, known := t.describeElement(a.Ref)
	target := fmt.Sprintf("element %d", a.Ref)
	if known {
		target = Brief(el)
	}
	out := fmt.Sprintf("fill the %s of the saved credential %q into %s", a.Field, a.Credential, target)
	if url != "" {
		out += " on " + clip(SafeURL(url), 120)
	}
	if cred, ok := t.creds.Get(a.Credential); ok && url != "" {
		if err := cred.AllowsURL(url); err != nil {
			out += " (REFUSED: " + err.Error() + ")"
		} else {
			out += " (a site it is saved for)"
		}
	}
	return out
}
