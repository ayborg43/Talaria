package provider

import "fmt"

// Provider types accepted by New.
const (
	TypeOpenAI    = "openai"    // any OpenAI-compatible /chat/completions API
	TypeAnthropic = "anthropic" // the native Anthropic Messages API
	TypeDeepSeek  = "deepseek"  // DeepSeek's API: OpenAI-compatible, plus its reasoning_content and thinking settings
)

// Spec says how to reach a model API.
type Spec struct {
	Type      string // TypeOpenAI (default) or TypeAnthropic
	BaseURL   string // default depends on Type
	APIKey    string
	MaxTokens int // Anthropic only: per-reply limit
	// NoVision says the model cannot see images. They are then left out of what
	// is sent, with a note in their place, instead of the API refusing the request.
	NoVision bool
	// Thinking and ReasoningEffort are DeepSeek's settings. Thinking is "" (the
	// service's default, on), "enabled" or "disabled"; ReasoningEffort is ""
	// (the service's default), "low", "high" or "max".
	Thinking        string
	ReasoningEffort string
}

// DefaultBaseURL is the hosted endpoint for a provider type.
func DefaultBaseURL(typ string) string {
	switch typ {
	case TypeAnthropic:
		return "https://api.anthropic.com"
	case TypeDeepSeek:
		return "https://api.deepseek.com"
	}
	return "https://api.openai.com/v1"
}

func New(s Spec) (Provider, error) {
	base := s.BaseURL
	if base == "" {
		base = DefaultBaseURL(s.Type)
	}
	switch s.Type {
	case "", TypeOpenAI:
		o := NewOpenAI(base, s.APIKey)
		o.NoVision = s.NoVision
		return o, nil
	case TypeDeepSeek:
		o := NewOpenAI(base, s.APIKey)
		o.NoVision = s.NoVision
		o.PassReasoning = true
		o.Thinking, o.ReasoningEffort = s.Thinking, s.ReasoningEffort
		return o, nil
	case TypeAnthropic:
		a := NewAnthropic(base, s.APIKey, s.MaxTokens)
		a.NoVision = s.NoVision
		return a, nil
	}
	return nil, fmt.Errorf("unknown provider type %q (use %q, %q or %q)", s.Type, TypeOpenAI, TypeAnthropic, TypeDeepSeek)
}
