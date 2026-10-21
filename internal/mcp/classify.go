package mcp

import (
	"regexp"
	"strings"
	"unicode"
)

// Class is what kind of thing a tool does, which decides whether it may run
// without asking.
type Class int

const (
	// ClassOther is anything not recognised: it asks.
	ClassOther Class = iota
	// ClassRead only looks: search, list, get.
	ClassRead
	// ClassDraft prepares something without sending it: a draft email.
	ClassDraft
	// ClassSend reaches other people or the outside world: send, post, reply, share, pay.
	ClassSend
	// ClassDelete destroys: delete, remove, trash, cancel.
	ClassDelete
)

func (c Class) String() string {
	return [...]string{"other", "read", "draft", "send", "delete"}[c]
}

// Sensitive reports whether a class is one that is always asked about.
func (c Class) Sensitive() bool { return c == ClassSend || c == ClassDelete }

// Annotations are a server's own description of what a tool does (MCP tool
// annotations). They are hints from the server, not facts: a server can say a
// tool only reads when it does not, so they are only ever used to be more
// careful, or to confirm what the tool's name already says.
type Annotations struct {
	ReadOnly    *bool `json:"readOnlyHint"`
	Destructive *bool `json:"destructiveHint"`
	OpenWorld   *bool `json:"openWorldHint"`
}

var (
	sendWords = set("send", "post", "publish", "reply", "forward", "share", "invite", "pay", "transfer", "submit", "broadcast", "notify", "tweet", "email", "mail", "text", "sms", "call", "dm")
	// Words that are as often a thing as an action: "get_post" and "list_mail" read, "send_mail" sends.
	// They only count as sending when the tool does not start with a reading verb (see Classify).
	nounish = set("email", "mail", "text", "sms", "call", "dm", "post", "reply", "share")
	// Words for reaching out over the network: whatever the tool reads, what it was asked for goes
	// to someone else. Such a tool is not a plain read.
	netWords    = set("url", "uri", "http", "https", "web", "website", "webpage", "curl", "wget", "browse", "crawl", "scrape", "download", "export", "request", "proxy")
	delWords    = set("delete", "remove", "trash", "purge", "drop", "destroy", "cancel", "revoke", "erase", "clear", "empty", "wipe", "unsubscribe", "kick", "ban")
	readVerbs   = set("get", "list", "search", "read", "fetch", "find", "query", "show", "view", "check", "lookup", "count", "describe", "browse", "export", "download", "preview", "inspect", "summarize", "who", "whoami")
	writeVerbs  = set("create", "update", "set", "add", "write", "edit", "modify", "move", "mark", "label", "patch", "put", "insert", "upload", "save", "rename", "copy", "archive", "star", "snooze", "accept", "decline", "respond", "approve", "reject", "reschedule")
	camelBreaks = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// words splits a tool name into lower-case words: list_events, listEvents and
// list-events all become [list events].
func words(name string) []string {
	name = camelBreaks.ReplaceAllString(name, "$1 $2")
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func isTrue(b *bool) bool { return b != nil && *b }

// Classify decides what a tool does from its name and the server's hints,
// leaning towards caution: when the two disagree, or the name is not
// recognised, the answer is the one that asks.
func Classify(name string, a Annotations) Class {
	ws := words(name)
	has := func(m map[string]bool) bool {
		for _, w := range ws {
			if m[w] {
				return true
			}
		}
		return false
	}
	first := ""
	if len(ws) > 0 {
		first = ws[0]
	}
	reader := readVerbs[first]
	compound := has(set("and", "then", "also", "after", "before")) // "fetch_and_post": two actions, and the last one counts
	plainReader := reader && !compound

	switch {
	case has(delWords) || isTrue(a.Destructive):
		return ClassDelete
	case has(sendWords) && !(plainReader && !isSendVerb(first) && onlyNounish(ws)):
		// "send_email" sends; "search_email" and "list_mail" read. A word like email only means
		// sending when the tool does not start with a reading verb. A reading verb does not excuse a
		// word that is only a verb ("fetch_and_post_message", "read_and_reply"), or a tool that does
		// two things.
		return ClassSend
	case isTrue(a.OpenWorld) && !isTrue(a.ReadOnly):
		if reader && !has(writeVerbs) {
			return ClassOther // the name says it reads and the hint says it reaches outside: they disagree, so it asks
		}
		return ClassSend // the server says it reaches outside, and the name does not say it only reads
	case has(netWords):
		return ClassOther // reaches out over the network: asks, whatever it says it does
	case !compound && has(readVerbs) && !has(writeVerbs):
		// The name says it reads. A server's readOnlyHint agrees with that, or is ignored: a name
		// that says nothing is not made a read by a hint, which the server could set on anything.
		return ClassRead
	case has(set("draft", "drafts")) && !isTrue(a.ReadOnly):
		return ClassDraft
	}
	return ClassOther
}

// isSendVerb reports whether a first word is itself one that sends.
func isSendVerb(w string) bool { return w != "" && sendWords[w] && !readVerbs[w] }

// onlyNounish reports whether every sending word in a tool's name is one that is also a thing.
func onlyNounish(ws []string) bool {
	for _, w := range ws {
		if sendWords[w] && !nounish[w] {
			return false
		}
	}
	return true
}
