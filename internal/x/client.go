// Package x talks to X (formerly Twitter) through its v2 API: it reads the
// mentions of the signed-in account and posts, or replies, as it. The token
// is a user-context access token, pasted in Settings like the chat bot
// tokens; posting always asks the user first.
package x

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API is the X v2 API. Tests point the client at a local server instead.
const API = "https://api.x.com/2"

// Client calls the X v2 API with a user's access token.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return API
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

type apiError struct {
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

func (e *apiError) Error() string {
	if e.Detail != "" {
		return e.Message + ": " + e.Detail
	}
	return e.Message
}

// do sends one request and decodes the data envelope into out.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	u := c.base() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("X is not answering: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var envelope struct {
			Errors []apiError `json:"errors"`
			Title  string     `json:"title"`
			Detail string     `json:"detail"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			if len(envelope.Errors) > 0 {
				return fmt.Errorf("X says no: %s", envelope.Errors[0].Error())
			}
			if envelope.Title != "" {
				msg := envelope.Title
				if envelope.Detail != "" {
					msg += ": " + envelope.Detail
				}
				return fmt.Errorf("X says no: %s", msg)
			}
		}
		return fmt.Errorf("X says no: HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data     json.RawMessage `json:"data"`
		Includes json.RawMessage `json:"includes"`
		Meta     json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("X answered in a shape this does not understand: %w", err)
	}
	if out != nil {
		switch t := out.(type) {
		case *envelopeOut:
			t.Data = envelope.Data
			t.Includes = envelope.Includes
			t.Meta = envelope.Meta
			return nil
		}
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("X answered without data")
	}
	return json.Unmarshal(envelope.Data, out)
}

type envelopeOut struct {
	Data     json.RawMessage
	Includes json.RawMessage
	Meta     json.RawMessage
}

// Me is the signed-in account.
type Me struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Verify checks the token and names who it signs in as.
func (c *Client) Verify(ctx context.Context) (Me, error) {
	var me Me
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, nil, &me); err != nil {
		return Me{}, err
	}
	if me.ID == "" {
		return Me{}, fmt.Errorf("X answered without an account")
	}
	return me, nil
}

// Tweet is one post.
type Tweet struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	AuthorID  string `json:"author_id"`
	CreatedAt string `json:"created_at"`
}

// Mentions lists the recent mentions of userID, newest first. sinceID, when
// given, leaves out everything up to and including that mention.
func (c *Client) Mentions(ctx context.Context, userID, sinceID string, max int) ([]Tweet, map[string]string, error) {
	if max <= 0 || max > 100 {
		max = 10
	}
	q := url.Values{}
	q.Set("max_results", fmt.Sprint(max))
	if sinceID != "" {
		q.Set("since_id", sinceID)
	}
	q.Set("tweet.fields", "created_at,author_id")
	q.Set("expansions", "author_id")
	q.Set("user.fields", "username")
	var env envelopeOut
	if err := c.do(ctx, http.MethodGet, "/users/"+userID+"/mentions", q, nil, &env); err != nil {
		return nil, nil, err
	}
	var tweets []Tweet
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &tweets); err != nil {
			return nil, nil, fmt.Errorf("X answered in a shape this does not understand: %w", err)
		}
	}
	authors := map[string]string{}
	if len(env.Includes) > 0 {
		var inc struct {
			Users []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"users"`
		}
		if err := json.Unmarshal(env.Includes, &inc); err == nil {
			for _, u := range inc.Users {
				authors[u.ID] = u.Username
			}
		}
	}
	return tweets, authors, nil
}

// Posted is what posting returns.
type Posted struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Post publishes text, or replies to replyTo when given.
func (c *Client) Post(ctx context.Context, text, replyTo string) (Posted, error) {
	payload := map[string]any{"text": text}
	if replyTo != "" {
		payload["reply"] = map[string]string{"in_reply_to_tweet_id": replyTo}
	}
	var posted Posted
	if err := c.do(ctx, http.MethodPost, "/tweets", nil, payload, &posted); err != nil {
		return Posted{}, err
	}
	if posted.ID == "" {
		return Posted{}, fmt.Errorf("X did not say what it published")
	}
	return posted, nil
}
