package tool

import (
	"net/url"
	"regexp"
	"strings"
)

// Risk is what a call could do that its owner may want to decide on beforehand. A call with no risk
// changes nothing that cannot be put back and says nothing in public.
type Risk uint8

const (
	// RiskDeletes: the call removes something that is not easily put back: a file, a saved skill or schedule,
	// a record in a connected service.
	RiskDeletes Risk = 1 << iota
	// RiskPosts: the call says something in public, as the user: a post, a reply, a comment on a social site.
	RiskPosts
)

// Note says what a risk is, for the person being asked.
func (r Risk) Note() string {
	switch {
	case r&RiskDeletes != 0 && r&RiskPosts != 0:
		return "This deletes something and posts in public."
	case r&RiskDeletes != 0:
		return "This deletes something."
	case r&RiskPosts != 0:
		return "This posts on social media, as you."
	}
	return ""
}

// ---- shell commands ----

// A command is taken to delete something if any word in it is a command that removes, or it matches one of
// the patterns below. This is a net, and it errs towards asking: a word like rm inside an echoed string asks
// too. What it cannot see is a deletion done by a program the command only runs (a script that removes files
// when it is run), or a command built up so that it does not read as one.
var deleteWords = map[string]bool{
	"rm": true, "rmdir": true, "unlink": true, "shred": true, "srm": true, "trash": true, "trash-put": true, "gio-trash": true,
	"rimraf": true, "del": true, "erase": true, "truncate": true, "mkfs": true, "wipefs": true, "wipe": true, "remove-item": true,
	"flushall": true, "flushdb": true, "-delete": true, "--delete": true, "--delete-after": true, "--delete-before": true,
	"--delete-during": true, "--delete-excluded": true, "--remove-source-files": true, "--purge": true, "--prune": true,
}

var wordRE = regexp.MustCompile(`[a-z0-9_.+\-]+`)

var deletePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgit\s+(clean|reset\s+(--hard|--merge)|stash\s+(drop|clear)|branch\s+-[a-z]*d|tag\s+-[a-z]*d|checkout\s+(--\s|\.\s*$|-f\b)|push\s+.*(--delete|--force|-f\b|\s:\S))`),
	regexp.MustCompile(`\b(docker|podman)\s+(rm|rmi|kill|system\s+prune|(volume|container|image|network)\s+(rm|prune)|compose\s+down\s+.*-v)`),
	regexp.MustCompile(`\b(kubectl|oc)\s+(delete|drain)`),
	regexp.MustCompile(`\b(helm)\s+(uninstall|delete)`),
	regexp.MustCompile(`\bterraform\s+(destroy|apply\s+.*-destroy)`),
	regexp.MustCompile(`\b(aws|gcloud|az|doctl|heroku|fly|flyctl|vercel|netlify|railway)\b.*\b(rm|delete|remove|terminate|destroy|purge|drop)\b`),
	regexp.MustCompile(`\bgh\s+\S+\s+delete`),
	regexp.MustCompile(`\b(npm|pnpm|yarn|pip3?|pipx|brew|cargo|gem|go)\s+(uninstall|unpublish|remove|rm)\b`),
	regexp.MustCompile(`\b(apt(-get)?|dnf|yum|apk|pacman|snap|zypper)\s+(remove|purge|autoremove|del|-r\b)`),
	regexp.MustCompile(`\bdrop\s+(table|database|schema|index|view|user|role|collection)\b`),
	regexp.MustCompile(`\bdelete\s+from\b`),
	regexp.MustCompile(`\btruncate\s+(table\b|-s\b)`),
	regexp.MustCompile(`\bdd\b.*\bof=`),
	regexp.MustCompile(`\b(os\.(remove|unlink|rmdir|removedirs)|shutil\.rmtree|rmtree\(|\.unlink\(|fs\.(rm|unlink|rmdir)\b|\b(unlinksync|rmsync|rmdirsync)\(|file\.delete|files\.delete|fileutils\.rm|remove_dir_all|remove_file)`),
	regexp.MustCompile(`>\s*/dev/(sd|nvme|disk)`),
	regexp.MustCompile(`\bfind\b.*-exec\s+(rm|unlink|shred)`),
	regexp.MustCompile(`\bmv\b.*\s/dev/null\b`),
}

var restoreRE = regexp.MustCompile(`\bgit\s+restore\b`)

// ShellRisk says whether a shell command deletes something.
func ShellRisk(command string) Risk {
	c := strings.ToLower(command)
	for _, w := range wordRE.FindAllString(c, -1) {
		if i := strings.LastIndex(w, "/"); i >= 0 { // /bin/rm, ./rm
			w = w[i+1:]
		}
		if deleteWords[w] || strings.HasPrefix(w, "mkfs.") {
			return RiskDeletes
		}
	}
	for _, re := range deletePatterns {
		if re.MatchString(c) {
			return RiskDeletes
		}
	}
	// git restore throws away changes in the working files; with --staged alone it only takes them out of the
	// next commit, which loses nothing.
	if restoreRE.MatchString(c) && (!strings.Contains(c, "--staged") || strings.Contains(c, "--worktree")) {
		return RiskDeletes
	}
	return 0
}

// ---- social media ----

// socialHosts are sites where what is typed or clicked is, or becomes, a public post, reply or message in the
// user's name. A page on one of them, and any of its subdomains, counts.
var socialHosts = []string{
	"x.com", "twitter.com", "facebook.com", "fb.com", "messenger.com", "instagram.com", "threads.net", "threads.com",
	"linkedin.com", "tiktok.com", "reddit.com", "redd.it", "bsky.app", "bsky.social", "pinterest.com", "tumblr.com",
	"snapchat.com", "mastodon.social", "web.whatsapp.com", "web.telegram.org", "discord.com", "quora.com", "vk.com",
}

// SocialHost says whether a host is a social media site, or one of extra, which the user can add to the list.
func SocialHost(host string, extra []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, list := range [][]string{socialHosts, extra} {
		for _, s := range list {
			s = strings.ToLower(strings.TrimSpace(s))
			if s != "" && (host == s || strings.HasSuffix(host, "."+s)) {
				return true
			}
		}
	}
	return false
}

// SocialURL says whether an address is on a social media site.
func SocialURL(raw string, extra []string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return SocialHost(u.Hostname(), extra)
}

var postWords = regexp.MustCompile(`(?i)(^|[_\-\s.])(post|tweet|retweet|repost|toot|publish|share|comment|reply|thread|story|dm)([_\-\s.]|$)`)

// socialServers are the names a connected service may have when it is a social network or a tool for posting to
// one. A longer name matches anywhere in the server's name (my-linkedin-bridge); a short one only exactly.
var socialServers = []string{"x", "twitter", "facebook", "instagram", "threads", "linkedin", "mastodon", "bluesky", "bsky", "tiktok", "reddit",
	"pinterest", "tumblr", "social", "buffer", "hootsuite", "typefully", "fediverse"}

// A tool that names itself as a social post, whatever its server is called.
var socialNames = regexp.MustCompile(`(?i)(tweet|toot|retweet|repost|social)`)

// LooksLikePost says whether a connected service's tool, going by its name and its server's, posts on social
// media. A connected service says little about what its tools do, so this is a reading of the names.
func LooksLikePost(server, name string) bool {
	if !postWords.MatchString(name) && !socialNames.MatchString(name) {
		return false
	}
	srv := strings.ToLower(server)
	for _, s := range socialServers {
		if srv == s || len(s) > 3 && strings.Contains(srv, s) {
			return true
		}
	}
	return socialNames.MatchString(name)
}
