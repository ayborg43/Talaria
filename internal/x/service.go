package x

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits from X: a post holds 280 characters, and a mentions page holds 100.
const (
	maxPost = 280
	maxPage = 100
)

// Service is X as the agent sees it: who it is signed in as, the mentions,
// and posting. Nil turns the X tools off.
type Service struct {
	Client *Client
	now    func() time.Time
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Verify names the account the token signs in as, for the Settings test.
func (s *Service) Verify(ctx context.Context) (string, error) {
	me, err := s.Client.Verify(ctx)
	if err != nil {
		return "", err
	}
	name := me.Username
	if name != "" {
		name = "@" + name
	} else {
		name = me.Name
	}
	return fmt.Sprintf("Posting as %s.", name), nil
}

// RecentMentions is the text x_mentions returns: the newest mentions first,
// each with its id for x_post's reply_to.
func (s *Service) RecentMentions(ctx context.Context, sinceID string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > maxPage {
		limit = maxPage
	}
	me, err := s.Client.Verify(ctx)
	if err != nil {
		return "", err
	}
	tweets, authors, err := s.Client.Mentions(ctx, me.ID, strings.TrimSpace(sinceID), limit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Mentions, newest first (times in UTC):\n\n")
	if len(tweets) == 0 {
		b.WriteString("No mentions.\n")
		return b.String(), nil
	}
	for _, t := range tweets {
		author := authors[t.AuthorID]
		if author == "" {
			author = t.AuthorID
		}
		when := t.CreatedAt
		if parsed, perr := time.Parse(time.RFC3339, t.CreatedAt); perr == nil {
			when = parsed.UTC().Format("Mon 2 Jan 15:04")
		}
		fmt.Fprintf(&b, "id %s  @%s  %s\n%s\n\n", t.ID, author, when, firstLines(t.Text, 500))
	}
	b.WriteString("Reply with x_post and reply_to set to the mention's id.\n")
	return b.String(), nil
}

// Post publishes text, or replies to replyTo when given, and says where it landed.
func (s *Service) Post(ctx context.Context, text, replyTo string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("there is nothing to post: text is empty")
	}
	if utf8.RuneCountInString(text) > maxPost {
		return "", fmt.Errorf("that is %d characters: X holds at most %d", utf8.RuneCountInString(text), maxPost)
	}
	replyTo = strings.TrimSpace(replyTo)
	for _, r := range replyTo {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("reply_to %q is not a mention id from x_mentions", replyTo)
		}
	}
	posted, err := s.Client.Post(ctx, text, replyTo)
	if err != nil {
		return "", err
	}
	if replyTo != "" {
		return fmt.Sprintf("Replied as post %s.", posted.ID), nil
	}
	return fmt.Sprintf("Posted as %s.", posted.ID), nil
}

func firstLines(text string, max int) string {
	lines := strings.Split(text, "\n")
	var kept []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		kept = append(kept, l)
		if len(kept) == 3 {
			break
		}
	}
	out := strings.Join(kept, "\n")
	if utf8.RuneCountInString(out) > max {
		runes := []rune(out)
		out = string(runes[:max]) + "…"
	}
	if out == "" {
		return "(no text)"
	}
	return out
}
