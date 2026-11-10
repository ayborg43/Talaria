package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"talaria/internal/tool"
)

// Verdicts triage gives a message.
const (
	NeedsYou = "needs"
	CanWait  = "wait"
)

// Triaged is one message with where it belongs and why. The reason is a short
// sentence the user can read and disagree with, never a score.
type Triaged struct {
	Summary
	Class  string
	Reason string
}

var (
	noReplyFrom = regexp.MustCompile(`(?i)(^|[<\s"._-])(no[-_.]?reply|do[-_.]?not[-_.]?reply|donotreply|mailer-daemon|notifications?|alerts?|newsletter|updates?|news|marketing|digest)@`)
	// asks is wording that wants something from the reader.
	asks = regexp.MustCompile(`(?i)\b(please|can you|could you|would you|need(s|ed)? (you|your)|your (review|approval|input|feedback|signature|reply|response)|review|approve|approval|confirm|sign|rsvp|reply|respond|let me know|question|follow[- ]?up|asap|urgent|deadline|due (today|tomorrow|on|by)|by (eod|end of day|tomorrow|friday|monday)|action required)\b`)
	// urgent is what is worth interrupting for even when a program sent it.
	urgent = regexp.MustCompile(`(?i)\b(security alert|suspicious (sign[- ]?in|activity)|new sign[- ]?in|verify your|verification code|password reset|payment (failed|declined|overdue)|failed payment|overdue|past due|final notice|action required|your account (has been|is) (locked|suspended|limited)|expires? (today|tomorrow|soon)|invoice due)\b`)
	// receipts is routine paperwork: kept, not read.
	receipts = regexp.MustCompile(`(?i)\b(receipt|your order|order (confirmation|#|number)|has shipped|shipped|out for delivery|delivered|tracking|statement is ready|thanks for your (purchase|order|payment)|payment (received|successful|confirmation))\b`)
	invite   = regexp.MustCompile(`(?i)^(updated )?invitation[: ]`)
	accepted = regexp.MustCompile(`(?i)^(accepted|declined|tentative(ly accepted)?)[: ]`)
)

func noReply(from string) bool { return noReplyFrom.MatchString(from) }

// addressedTo says whether the message names me in To.
func addressedTo(to, me string) bool {
	me = strings.ToLower(strings.TrimSpace(me))
	return me != "" && strings.Contains(strings.ToLower(to), me)
}

func firstMatch(re *regexp.Regexp, s string) string {
	return strings.ToLower(strings.TrimSpace(re.FindString(s)))
}

// Classify decides, from what a listing shows, whether a message needs the
// user or can wait, and says why in a sentence. It looks at who sent it, who
// it went to, whether a person or a program wrote it, and what the subject
// asks. It reads no message bodies and calls no model, so it is cheap, the
// same every time, and its reasons are true by construction. Where it is
// unsure it errs toward "needs you": a message wrongly shown costs a glance;
// one wrongly held back costs the user.
func Classify(m Summary, me string) (class, reason string) {
	subj := m.Subject
	switch {
	case m.Flagged:
		return NeedsYou, "You flagged it."
	case accepted.MatchString(subj):
		return CanWait, "A reply to an invitation you sent."
	case invite.MatchString(subj):
		return NeedsYou, "A calendar invitation waiting for your answer."
	}
	if w := firstMatch(urgent, subj); w != "" {
		if m.Auto || m.Bulk {
			return NeedsYou, fmt.Sprintf("From a program, but it says %q.", w)
		}
		return NeedsYou, fmt.Sprintf("Says %q.", w)
	}
	switch {
	case m.Bulk:
		return CanWait, "A newsletter or mailing list."
	case m.Auto:
		if receipts.MatchString(subj) {
			return CanWait, "A receipt or order update."
		}
		return CanWait, "Sent by a program, not a person."
	case receipts.MatchString(subj):
		return CanWait, "A receipt or order update."
	case m.Answered:
		return CanWait, "You have already replied."
	}
	direct := addressedTo(m.To, me)
	copied := !direct && addressedTo(m.Cc, me)
	switch {
	case copied:
		return CanWait, "You are only copied."
	case direct && asks.MatchString(subj):
		return NeedsYou, fmt.Sprintf("A person wrote to you directly and %q.", askWord(subj))
	case direct && strings.Contains(subj, "?"):
		return NeedsYou, "A person wrote to you directly and asks a question."
	case direct && m.Unread:
		return NeedsYou, "A person wrote to you directly, and you have not read it."
	case direct:
		return CanWait, "Written to you, and you have already read it."
	case asks.MatchString(subj) && m.Unread:
		return NeedsYou, fmt.Sprintf("Unread, and %q.", askWord(subj))
	case m.Unread:
		return NeedsYou, "A person wrote it and you have not read it."
	}
	return CanWait, "Already read, nothing in it asks for you."
}

func askWord(subj string) string {
	if w := firstMatch(asks, subj); w != "" {
		return "mentions " + w
	}
	return "asks"
}

// Triage lists recent mail and sorts it: what needs the user first, each with
// its reason, then what can wait.
func (s *Service) Triage(ctx context.Context, days, limit int) (needs, wait []Triaged, total int, err error) {
	if days <= 0 {
		days = 2
	}
	if limit <= 0 {
		limit = 30
	}
	list, total, err := s.Search(ctx, Query{Days: days, Limit: min(limit, maxLimit)})
	if err != nil {
		return nil, nil, 0, err
	}
	for _, m := range list {
		class, why := Classify(m, s.Address)
		t := Triaged{Summary: m, Class: class, Reason: why}
		if class == NeedsYou {
			needs = append(needs, t)
		} else {
			wait = append(wait, t)
		}
	}
	return needs, wait, total, nil
}

func (s *Service) formatTriage(needs, wait []Triaged, total, days int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mail from the last %d day(s), sorted by rules (who it went to, whether a person or a program sent it, what the subject asks). Times in %s.\n", days, s.zone())
	line := func(t Triaged) {
		mark := " "
		if t.Unread {
			mark = "*"
		}
		fmt.Fprintf(&b, "id %d %s %s  From: %s  Subject: %s\n    why: %s\n", t.UID, mark, s.when(t.Date), orNone(t.From, "(unknown)"), orNone(t.Subject, "(no subject)"), t.Reason)
	}
	fmt.Fprintf(&b, "\nNEEDS YOU (%d):\n", len(needs))
	if len(needs) == 0 {
		b.WriteString("nothing.\n")
	}
	for _, t := range needs {
		line(t)
	}
	fmt.Fprintf(&b, "\nCAN WAIT (%d):\n", len(wait))
	if len(wait) == 0 {
		b.WriteString("nothing.\n")
	}
	for _, t := range wait {
		line(t)
	}
	if total > len(needs)+len(wait) {
		fmt.Fprintf(&b, "\n(%d older messages in this window are not shown.)\n", total-len(needs)-len(wait))
	}
	b.WriteString("\n* = unread. The reasons come from headers and subjects only: open a message with email_read before judging it.\n")
	return b.String()
}

func triageItems(list []Triaged) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{"id": t.UID, "unread": t.Unread, "from": t.From, "subject": t.Subject, "why": t.Reason, "at": t.Date})
	}
	return out
}

// RegisterTriage adds email_triage.
func RegisterTriage(r *tool.Registry, s *Service) {
	r.Register(tool.Tool{
		Name: "email_triage",
		Description: "Sort the user's recent mail into what needs them and what can wait, with a one-line reason for each. Read-only. " +
			"The reasons come from who the message went to, whether a person or a program sent it, and the subject, so check a message with email_read before acting on it. " +
			"Use it for 'what needs me?', 'summarise my inbox' and the morning brief. " + untrustedNote,
		Schema: json.RawMessage(`{"type":"object","properties":{
			"days":{"type":"integer","description":"how many days back to look, 1 to 14; default 2"},
			"limit":{"type":"integer","description":"how many messages to sort, 1 to 30; default 30"}}}`),
		Untrusted: true,
		RunRich: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var a struct{ Days, Limit int }
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return tool.Result{}, err
				}
			}
			if a.Days < 0 || a.Days > 14 || a.Limit < 0 || a.Limit > maxLimit {
				return tool.Result{}, fmt.Errorf("days must be 1 to 14 and limit 1 to %d", maxLimit)
			}
			if a.Days == 0 {
				a.Days = 2
			}
			needs, wait, total, err := s.Triage(ctx, a.Days, a.Limit)
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{
				Text:     s.formatTriage(needs, wait, total, a.Days),
				Artifact: tool.Artifact("mail_triage", map[string]any{"needs": triageItems(needs), "wait": triageItems(wait)}),
			}, nil
		},
	})
}
