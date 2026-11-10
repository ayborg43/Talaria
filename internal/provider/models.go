package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// maxModels bounds how many model ids are read: a catalogue is a few dozen, and a hostile or broken server
// must not be able to make this hold more.
const maxModels = 500

// ListModels asks a provider which models this key can use: GET {base}/models, which every OpenAI-compatible
// service answers, Meta Model API and DeepSeek among them, and Anthropic at /v1/models. The ids come back
// sorted. It is what to look at when a model's name is refused: the name is wrong, or the key has no access to it.
func ListModels(ctx context.Context, s Spec) ([]string, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL(s.Type)
	}
	url := base + "/models"
	if s.Type == TypeAnthropic {
		url = base + "/v1/models?limit=1000"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case s.Type == TypeAnthropic:
		req.Header.Set("x-api-key", s.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
	case s.APIKey != "":
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	resp, err := newHTTPClient().Do(req) // redirects are not followed: they would carry the key along
	if err != nil {
		return nil, errors.New("the provider could not be reached to list its models")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("the provider's list of models could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the provider answered %s to a request for its models", resp.Status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("the provider's list of models was not in the usual form")
	}
	ids := make([]string, 0, min(len(out.Data), maxModels))
	for _, m := range out.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || len(id) > 200 || strings.ContainsAny(id, "\r\n\t") {
			continue
		}
		if len(ids) == maxModels {
			break
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
