package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/config"
	"talaria/internal/vault"
)

// vaultTestServer wires a Server to a vault in a blank directory: the vault
// file is made by seeding, and the passphrase comes from the environment like
// it does where Talaria runs unattended.
func vaultTestServer(t *testing.T, passphrase string) (*Server, string) {
	t.Helper()
	vault.ForTests(t)
	dir := t.TempDir()
	s := &Server{
		Cfg:        &config.Config{},
		ConfigPath: filepath.Join(dir, "config.json"),
		Getenv: func(k string) string {
			if k == "TALARIA_VAULT_PASSPHRASE" {
				return passphrase
			}
			return ""
		},
	}
	return s, filepath.Join(dir, "vault.enc")
}

func seedVault(t *testing.T, path, passphrase string) {
	t.Helper()
	v, err := vault.Create(path, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Add(vault.Credential{Name: "x", Kind: vault.KindLogin, Username: "me", Secret: "s3cret-pw", Sites: []string{"x.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := v.Add(vault.Credential{Name: "api", Kind: vault.KindSecret, Secret: "s3cret-key", Sites: []string{"api.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := v.Add(vault.Credential{Name: "mcp", Kind: vault.KindOAuth, Sites: []string{"mcp.example.com"},
		OAuth: &vault.OAuthData{TokenEndpoint: "https://mcp.example.com/token", ClientID: "c", AccessToken: "s3cret-tok"}}); err != nil {
		t.Fatal(err)
	}
}

func getSettings(t *testing.T, s *Server) (int, settingsView, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.settingsGet(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var view settingsView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return rec.Code, view, rec.Body.String()
}

func postSettings(t *testing.T, s *Server, payload string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.settingsPost(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload)))
	return rec.Code, rec.Body.String()
}

func TestSettingsGetListsVaultWithoutSecrets(t *testing.T) {
	s, path := vaultTestServer(t, "right-horse-battery")
	seedVault(t, path, "right-horse-battery")
	code, view, body := getSettings(t, s)
	if code != http.StatusOK {
		t.Fatalf("get settings: %d (%s)", code, body)
	}
	if view.VaultStatus != "" {
		t.Fatalf("vault status: %q", view.VaultStatus)
	}
	if len(view.Vault) != 2 {
		t.Fatalf("vault lists logins and secrets but not OAuth: %+v", view.Vault)
	}
	for _, c := range view.Vault {
		if c.Name == "x" && (c.Kind != vault.KindLogin || c.Username != "me" || len(c.Sites) != 1 || c.Sites[0] != "x.com") {
			t.Fatalf("login entry: %+v", c)
		}
		if c.Name == "api" && c.Kind != vault.KindSecret {
			t.Fatalf("secret entry: %+v", c)
		}
	}
	if strings.Contains(body, "s3cret") {
		t.Fatal("a secret reached the page")
	}
}

func TestSaveVaultCredentialAddAndDelete(t *testing.T) {
	s, path := vaultTestServer(t, "right-horse-battery")
	seedVault(t, path, "right-horse-battery")
	code, body := postSettings(t, s, `{"vault_credential":{"op":"add","name":"gh","username":"me","password":"pw123","sites":"github.com"}}`)
	if code != http.StatusOK {
		t.Fatalf("add: %d (%s)", code, body)
	}
	_, view, _ := getSettings(t, s)
	found := false
	for _, c := range view.Vault {
		if c.Name == "gh" {
			found = c.Kind == vault.KindLogin && c.Username == "me"
		}
	}
	if !found {
		t.Fatalf("added login is not listed: %+v", view.Vault)
	}
	code, body = postSettings(t, s, `{"vault_credential":{"op":"delete","name":"gh"}}`)
	if code != http.StatusOK {
		t.Fatalf("delete: %d (%s)", code, body)
	}
	_, view, _ = getSettings(t, s)
	for _, c := range view.Vault {
		if c.Name == "gh" {
			t.Fatalf("deleted login is still listed: %+v", view.Vault)
		}
	}
	code, body = postSettings(t, s, `{"vault_credential":{"op":"delete","name":"gh"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("deleting twice: %d (%s)", code, body)
	}
}

func TestSaveVaultCredentialValidation(t *testing.T) {
	s, path := vaultTestServer(t, "right-horse-battery")
	seedVault(t, path, "right-horse-battery")
	for _, payload := range []string{
		`{"vault_credential":{"op":"add","name":"gh","username":"me","password":"pw123"}}`,
		`{"vault_credential":{"op":"add","name":"!!!","password":"pw123","sites":"github.com"}}`,
		`{"vault_credential":{"op":"rename","name":"gh"}}`,
		`{"vault_credential":{"op":"delete"}}`,
	} {
		if code, body := postSettings(t, s, payload); code != http.StatusBadRequest {
			t.Fatalf("%s: %d (%s)", payload, code, body)
		}
	}
	// A name without a username saves a single key value, not a login.
	if code, body := postSettings(t, s, `{"vault_credential":{"op":"add","name":"api2","password":"pw123","sites":"github.com"}}`); code != http.StatusOK {
		t.Fatalf("add secret: %d (%s)", code, body)
	}
	_, view, _ := getSettings(t, s)
	found := false
	for _, c := range view.Vault {
		if c.Name == "api2" {
			found = c.Kind == vault.KindSecret && c.Username == ""
		}
	}
	if !found {
		t.Fatalf("added secret is not listed: %+v", view.Vault)
	}
}

func TestVaultLockedStatus(t *testing.T) {
	_, path := vaultTestServer(t, "right-horse-battery")
	seedVault(t, path, "right-horse-battery")

	locked, _ := vaultTestServer(t, "wrong-passphrase-here")
	if code, view, _ := getSettings(t, locked); code != http.StatusOK || len(view.Vault) != 0 || view.VaultStatus == "" {
		t.Fatalf("wrong passphrase: %d (%+v)", code, view)
	}
	if code, body := postSettings(t, locked, `{"vault_credential":{"op":"delete","name":"x"}}`); code != http.StatusBadRequest {
		t.Fatalf("saving while locked: %d (%s)", code, body)
	}

	missing, _ := vaultTestServer(t, "")
	if code, view, _ := getSettings(t, missing); code != http.StatusOK || view.VaultStatus == "" {
		t.Fatalf("no passphrase: %d (%+v)", code, view)
	}

	fresh, _ := vaultTestServer(t, "right-horse-battery")
	_ = path
	if code, view, _ := getSettings(t, fresh); code != http.StatusOK || !strings.Contains(view.VaultStatus, "tal vault init") {
		t.Fatalf("no vault file: %d (%+v)", code, view)
	}
	if code, body := postSettings(t, fresh, `{"vault_credential":{"op":"add","name":"gh","password":"pw","sites":"github.com"}}`); code != http.StatusBadRequest || !strings.Contains(body, "tal vault init") {
		t.Fatalf("saving with no vault: %d (%s)", code, body)
	}
}
