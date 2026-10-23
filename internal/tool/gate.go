package tool

import "time"

// Action is what to do with a tool call.
type Action int

const (
	Allow Action = iota // run it
	Ask                 // run it if the user approves
	Deny                // do not run it
)

// Verdict is a Gate's ruling on a call.
type Verdict struct {
	Action Action
	// Via says where the ruling came from, for the activity log: "auto" (the
	// tool's own default), "rule" (a permission rule), or "grant" (the user chose
	// to always allow this tool in this chat).
	Via  string
	Rule string // the rule that matched, if any
}

// Entry is one tool call as the activity log records it.
type Entry struct {
	Tool, Args string
	Label      string // the subagent that made the call, if one did
	Allowed    bool
	Via        string // "auto", "rule", "grant", "prompt", or "unknown tool"
	Rule       string
	Status     string // "ok", "error", or "not run"
	Result     string
	Took       time.Duration
}

// Gate is the permission layer around tools: it rules on each call before it
// runs, scrubs what comes back, and keeps a record. A nil Gate leaves things as
// they were: tools that need approval ask, the rest run.
type Gate interface {
	// Verdict rules on a call to the named tool. needsApproval is the tool's own
	// default: true means that, with no rule, it asks.
	Verdict(tool string, needsApproval bool) Verdict
	// Scrub removes secrets from text on its way to the model or to storage.
	Scrub(text string) string
	// Record notes a finished (or refused) call.
	Record(Entry)
}
