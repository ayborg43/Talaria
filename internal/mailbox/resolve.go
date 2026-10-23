package mailbox

import (
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

// imapHosts are the mail servers of the usual providers, found from the domain of the address, so that
// most people set only an address and an app password.
var imapHosts = map[string]string{
	"gmail.com": "imap.gmail.com", "googlemail.com": "imap.gmail.com",
	"icloud.com": "imap.mail.me.com", "me.com": "imap.mail.me.com", "mac.com": "imap.mail.me.com",
	"fastmail.com": "imap.fastmail.com", "fastmail.fm": "imap.fastmail.com",
	"yahoo.com": "imap.mail.yahoo.com", "aol.com": "imap.aol.com", "zoho.com": "imap.zoho.com",
	"outlook.com": "outlook.office365.com", "hotmail.com": "outlook.office365.com", "live.com": "outlook.office365.com",
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// Merge combines what Settings saved with the environment, which fills in
// whatever is left empty. What Settings saved wins, so it takes effect.
func Merge(savedAddress, savedUser, savedPassword, savedPasswordEnv, savedServer string, getenv func(string) string) (address, user, password, server string) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	password = firstNonEmpty(strings.TrimSpace(savedPassword), getenvOr(savedPasswordEnv, getenv), getenv("TALARIA_EMAIL_PASSWORD"))
	return firstNonEmpty(savedAddress, getenv("TALARIA_EMAIL")),
		firstNonEmpty(savedUser, getenv("TALARIA_EMAIL_USER")),
		password,
		firstNonEmpty(savedServer, getenv("TALARIA_EMAIL_SERVER"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func getenvOr(name string, getenv func(string) string) string {
	if name == "" || getenv == nil {
		return ""
	}
	return getenv(name)
}

// Resolve works out connection settings from merged values: what Settings
// saved with the environment filling what is left empty. It reports what is
// missing or wrong instead of connecting. configured is false when nothing is
// set at all.
func Resolve(address, password, server string) (addr, pw, srv string, problems []string, configured bool) {
	addr, pw, srv = strings.TrimSpace(address), strings.TrimSpace(password), strings.TrimSpace(server)
	if addr == "" && pw == "" && srv == "" {
		return "", "", "", nil, false
	}
	if addr == "" {
		problems = append(problems, "no address is set: put the mailbox address in Settings, or TALARIA_EMAIL")
	} else if a, err := mail.ParseAddress(addr); err != nil || a.Address != addr {
		problems = append(problems, "the address is not an email address: write it as you@example.com")
	} else {
		addr = a.Address
	}
	if pw == "" {
		problems = append(problems, "no app password is set: put one in Settings, or TALARIA_EMAIL_PASSWORD (an app password, not your normal password)")
	}
	if srv == "" && addr != "" {
		domain := ""
		if at := strings.LastIndex(addr, "@"); at >= 0 {
			domain = strings.ToLower(addr[at+1:])
			srv = imapHosts[domain]
		}
		if srv == "" {
			problems = append(problems, fmt.Sprintf("a server is needed for %s: put it in Settings, or TALARIA_EMAIL_SERVER (such as imap.example.com)", domain))
		}
	}
	if srv != "" {
		host, port := srv, "993"
		if h, p, ok := strings.Cut(srv, ":"); ok {
			host, port = h, p
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || !hostName.MatchString(host) {
			problems = append(problems, "the server must be a server name such as imap.example.com, with an optional :port (the connection is always encrypted)")
		} else {
			srv = host + ":" + port
		}
	}
	// Google shows app passwords in groups of four with spaces, and accepts them without.
	if strings.HasPrefix(srv, "imap.gmail.com:") {
		pw = strings.Join(strings.Fields(pw), "")
	}
	return addr, pw, srv, problems, true
}
