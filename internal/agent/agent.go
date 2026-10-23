// Package agent runs the model <-> tools loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"talaria/internal/activity"
	"time"

	"talaria/internal/metrics"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

const (
	defaultMaxSteps = 100
	maxToolOutput   = 32 << 10
)

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

// Labelled names a tool as a subagent's call to it appears in an approval
// prompt: "write_file (subagent notes)". BaseTool undoes it.
func Labelled(tool, label string) string { return tool + " (subagent " + label + ")" }

// afterOutside marks an approval prompt that is asked only because the conversation
// has read outside content.
const afterOutside = " (after reading outside content)"

// Flagged names a tool as it appears in a prompt that is asked because of what the
// conversation has read.
func Flagged(tool string) string { return tool + afterOutside }

// IsFlagged reports whether a prompt's tool name came from Flagged.
func IsFlagged(name string) bool { return strings.Contains(name, afterOutside) }

// BaseTool is the tool a possibly Labelled or Flagged name refers to.
func BaseTool(name string) string {
	name = strings.Replace(name, afterOutside, "", 1)
	if i := strings.Index(name, " (subagent "); i > 0 {
		return name[:i]
	}
	return name
}

// Agent holds one conversation and drives it to a final answer.
type Agent struct {
	Provider provider.Provider
	Model    string
	Tools    *tool.Registry
	System   string
	MaxSteps int
	// ToolTimeout bounds one tool call; default 10 minutes. A tool that
	// ignores cancellation cannot be killed, so when the time is up the call
	// is abandoned and the model is told it timed out, instead of wedging the
	// chat behind it. The run's own remaining time stretches the default, so a
	// long job budget is not cut short by it.
	ToolTimeout time.Duration
	// Context, if set, returns extra text for the system prompt. It is asked for
	// on every model call and is not kept in the conversation, so it can change
	// as the conversation goes on (for instance when the user moves the agent's
	// tools to another environment) without the history ever contradicting it.
	Context func() string

	// Approve is asked before any call the Gate says to ask about (by default,
	// tools with NeedsApproval). Nil means deny.
	Approve func(name, args string) bool
	// Gate, if set, rules on every tool call, scrubs results and keeps a record.
	Gate Gate
	// BeforeModel, if set, is asked before every request to the model, and may refuse it: this is
	// where a budget on model calls is enforced.
	BeforeModel func() error
	// GuardUntrusted makes calls that could send something out ask first once this
	// conversation has read outside content (see tool.Tool.Untrusted and Egress),
	// whatever would normally be allowed, except where a permission rule in the
	// config says "allow": that is the user's own explicit choice.
	GuardUntrusted bool
	// Label names this agent in the record when it is a subagent.
	Label string
	// OnToolStart, if set, is called just before a tool runs, after any approval.
	// Parallel tools run at once, so it may be called from several goroutines.
	OnToolStart func(name, args string)
	// OnToolCall, if set, is told about each tool call and its outcome.
	OnToolCall func(name, args, result string)
	// OnMessage, if set, sees every message added to the conversation (for persistence).
	OnMessage func(provider.Message)
	// Activity, if set, is told about the subagents this agent starts, for a front end that shows who is working.
	Activity activity.Sink
	// OnText, if set, makes the model stream: it receives reply text as it is
	// generated, including text said before tool calls.
	OnText func(delta string)

	messages []provider.Message
	tainted  int32 // set once an untrusted tool has run; atomic, as parallel tools update it
}

// MarkUntrusted records that the conversation has taken in text from outside, as
// when a watch hands the model what a service returned.
func (a *Agent) MarkUntrusted() { atomic.StoreInt32(&a.tainted, 1) }

// ReadOutside reports whether the conversation has taken in outside content.
func (a *Agent) ReadOutside() bool { return atomic.LoadInt32(&a.tainted) == 1 }

func (a *Agent) add(m provider.Message) {
	a.messages = append(a.messages, m)
	if a.OnMessage != nil {
		a.OnMessage(m)
	}
}

// Run appends the user's input, loops until the model answers without tool
// calls, and returns that final answer. History is kept across calls.
func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	if len(a.messages) == 0 && a.System != "" {
		a.messages = append(a.messages, provider.Message{Role: provider.RoleSystem, Content: a.System})
	}
	a.add(provider.Message{Role: provider.RoleUser, Content: input})

	steps := a.MaxSteps
	if steps <= 0 {
		steps = defaultMaxSteps
	}

	for i := 0; i < steps; i++ {
		if a.BeforeModel != nil {
			if err := a.BeforeModel(); err != nil {
				return "", err
			}
		}
		began := time.Now()
		reply, err := a.Provider.Chat(ctx, provider.Request{
			Model:    a.Model,
			Messages: a.requestMessages(),
			Tools:    a.Tools.Specs(),
			OnText:   a.OnText,
		})
		result := "ok"
		if err != nil {
			result = "error"
		}
		metrics.Inc("talaria_model_calls_total", "result", result)
		metrics.Observe("talaria_model_call", time.Since(began))
		if err != nil {
			return "", err
		}
		a.add(reply)
		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}

		a.runCalls(ctx, reply.ToolCalls)
	}
	return "", fmt.Errorf("stopped after %d steps without a final answer", steps)
}

// requestMessages is the conversation as the model should see it: the stored
// history, with Context added to the system prompt.
func (a *Agent) requestMessages() []provider.Message {
	history := a.dropOldImages(a.messages)
	if a.Context == nil {
		return history
	}
	extra := a.Context()
	if extra == "" {
		return history
	}
	msgs := slices.Clone(history) // the history itself stays as it was
	if len(msgs) > 0 && msgs[0].Role == provider.RoleSystem {
		msgs[0].Content += "\n\n" + extra
		return msgs
	}
	return append([]provider.Message{{Role: provider.RoleSystem, Content: extra}}, msgs...)
}

// maxImagesKept is how many of the most recent pictures the model is sent. A
// picture costs far more than the text around it, and the model has long since
// acted on an old screenshot; the stored conversation keeps them all.
const maxImagesKept = 2

const oldImageNote = "[an earlier image was left out to save space]"

// dropOldImages returns msgs with only the newest pictures left in.
func (a *Agent) dropOldImages(msgs []provider.Message) []provider.Message {
	keep := maxImagesKept
	var out []provider.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(msgs[i].Images) == 0 {
			continue
		}
		if keep > 0 {
			keep -= len(msgs[i].Images)
			continue
		}
		if out == nil {
			out = slices.Clone(msgs) // copy only when something must change
		}
		out[i].Images = nil
		out[i].Content = strings.TrimSpace(out[i].Content + "\n" + oldImageNote)
	}
	if out == nil {
		return msgs
	}
	return out
}

// runCalls executes one turn's tool calls. A run of consecutive Parallel tools
// goes at once; everything else runs one at a time, in order. Results are
// recorded in the order the model asked for them either way.
func (a *Agent) runCalls(ctx context.Context, calls []provider.ToolCall) {
	finish := func(call provider.ToolCall, result execResult) {
		if a.OnToolCall != nil {
			a.OnToolCall(call.Name, call.Arguments, result.text)
		}
		a.add(provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: result.text, Images: result.images})
	}

	// Before any runs, so calls made together are judged together. A tool that could itself send something
	// out is left to mark the conversation when it has run: marking it first would make it ask about itself.
	for _, call := range calls {
		if t, ok := a.Tools.Get(call.Name); ok && t.Untrusted && t.Egress == nil {
			a.MarkUntrusted()
		}
	}
	for i := 0; i < len(calls); {
		j := i
		for j < len(calls) && a.parallel(calls[j]) {
			j++
		}
		if j-i < 2 { // nothing to overlap with
			finish(calls[i], a.execute(ctx, calls[i]))
			i++
			continue
		}

		results := make([]execResult, j-i)
		var wg sync.WaitGroup
		for k := i; k < j; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[k-i] = a.execute(ctx, calls[k])
			}()
		}
		wg.Wait()
		for k := i; k < j; k++ {
			finish(calls[k], results[k-i])
		}
		i = j
	}
}

func (a *Agent) parallel(call provider.ToolCall) bool {
	t, ok := a.Tools.Get(call.Name)
	return ok && t.Parallel
}

// Call runs one tool call that did not come from the model, through the same
// rules as one that did (the Gate's verdict, scrubbing, the activity log), but
// without ever asking anyone: a call that would need approval is refused. A
// watch uses it to poll a service on its own. An error is the tool's own
// message, or why it was refused.
func (a *Agent) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	quiet := *a
	quiet.Approve = nil
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res := quiet.execute(ctx, provider.ToolCall{ID: "direct", Name: name, Arguments: string(args)})
	if msg, failed := strings.CutPrefix(res.text, "error: "); failed {
		return "", errors.New(msg)
	}
	return res.text, nil
}

// execResult is what one tool call came to: text, and any pictures.
type execResult struct {
	text   string
	images []provider.Image
}

// execute never returns an error: failures go back to the model as text so it can recover.
func (a *Agent) execute(ctx context.Context, call provider.ToolCall) (res execResult) {
	entry := Entry{Tool: call.Name, Args: call.Arguments, Label: a.Label, Status: "not run"}
	defer func() {
		metrics.Inc("talaria_tool_calls_total", "status", strings.ReplaceAll(entry.Status, " ", "_"))
		entry.Result = res.text
		if a.Gate != nil {
			a.Gate.Record(entry)
		}
	}()

	t, ok := a.Tools.Get(call.Name)
	if !ok {
		entry.Via = "unknown tool"
		return execResult{text: fmt.Sprintf("error: unknown tool %q", call.Name)}
	}

	if t.LogArgs != nil {
		entry.Args = t.LogArgs([]byte(call.Arguments))
	}
	needs := t.NeedsApproval || (t.NeedsApprovalFor != nil && t.NeedsApprovalFor(json.RawMessage(call.Arguments)))
	verdict := Verdict{Via: "auto"}
	if a.Gate != nil {
		verdict = a.Gate.Verdict(call.Name, needs)
	} else if needs {
		verdict.Action = Ask
	}
	flagged := false
	if a.GuardUntrusted && verdict.Action == Allow && verdict.Via != "rule" && a.ReadOutside() && t.Egress != nil && t.Egress(json.RawMessage(call.Arguments)) {
		verdict = Verdict{Action: Ask, Via: "outside content"}
		flagged = true
	}
	entry.Via, entry.Rule = verdict.Via, verdict.Rule
	switch verdict.Action {
	case Deny:
		return execResult{text: "error: the permission rules do not allow this tool"}
	case Ask:
		if !flagged {
			entry.Via = "prompt"
		}
		shown := call.Arguments
		if t.Describe != nil { // what the user is asked about is what the call will do, not its raw arguments
			if d := t.Describe([]byte(call.Arguments)); d != "" {
				shown = d
			}
		}
		name := call.Name
		if flagged {
			name = Flagged(name)
		}
		if a.Approve == nil || !a.Approve(name, shown) {
			metrics.Inc("talaria_approvals_total", "decision", "denied")
			return execResult{text: "error: the user denied this tool call"}
		}
		metrics.Inc("talaria_approvals_total", "decision", "allowed")
	}
	entry.Allowed = true

	defer func() { // a crashing tool must not take the whole agent down
		if r := recover(); r != nil {
			entry.Status = "error"
			res = execResult{text: fmt.Sprintf("error: the tool crashed: %v", r)}
		}
	}()
	if a.OnToolStart != nil {
		a.OnToolStart(call.Name, call.Arguments)
	}
	start := time.Now()
	out, err := a.executeTool(ctx, t, call.Arguments)
	entry.Took = time.Since(start)
	if err != nil {
		entry.Status = "error"
		return execResult{text: a.scrub("error: " + err.Error())}
	}
	entry.Status = "ok"
	if t.Untrusted {
		a.MarkUntrusted()
	}
	text := a.scrub(out.Text) // before cutting, so a secret is never left half-shown
	if len(text) > maxToolOutput {
		text = text[:maxToolOutput] + "\n[truncated]"
	}
	return execResult{text: text, images: out.Images}
}

func (a *Agent) scrub(s string) string {
	if a.Gate == nil {
		return s
	}
	return a.Gate.Scrub(s)
}

// defaultToolTimeout bounds one tool call when nothing else does: a chat's
// tools run without a deadline, and a tool that ignores cancellation must not
// wedge the chat behind it forever.
const defaultToolTimeout = 10 * time.Minute

// toolTimeout is how long one tool call may take: an explicit ToolTimeout, or
// the default stretched to the run's own remaining time, so a long job budget
// is not cut short by it.
func (a *Agent) toolTimeout(ctx context.Context) time.Duration {
	if a.ToolTimeout > 0 {
		return a.ToolTimeout
	}
	timeout := defaultToolTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > timeout {
			timeout = remaining
		}
	}
	return timeout
}

// executeTool runs one tool call with a timeout. A tool that ignores
// cancellation cannot be killed — nothing stops a goroutine — so when the time
// is up the call is abandoned: the model is told it timed out and the chat
// moves on, while the stray goroutine keeps the cancelled context and ends
// whenever it ends. A crash in the tool is reported, never propagated.
func (a *Agent) executeTool(ctx context.Context, t tool.Tool, args string) (tool.Result, error) {
	timeout := a.toolTimeout(ctx)
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type outcome struct {
		out tool.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: fmt.Errorf("the tool crashed: %v", r)}
			}
		}()
		out, err := t.Execute(tctx, []byte(args))
		done <- outcome{out, err}
	}()
	select {
	case o := <-done:
		return o.out, o.err
	case <-tctx.Done():
		if ctx.Err() != nil { // the run itself ended; the timeout did not fire
			return tool.Result{}, ctx.Err()
		}
		return tool.Result{}, fmt.Errorf("timed out after %s", timeout)
	}
}
