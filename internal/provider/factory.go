package provider

import "fmt"

// Provider types accepted by New.
const (
	TypeOpenAI    = "openai"    // any OpenAI-compatible /chat/completions API
	TypeAnthropic = "anthropic" // the native Anthropic Messages API
	TypeDeepSeek  = "deepseek"  // DeepSeek's API: OpenAI-compatible, plus its reasoning_content and thinking settings
	TypeMuse      = "muse"      // Meta Model API (the Muse models): OpenAI-compatible, plus a reasoning effort
)

// Types lists the provider types, in the order they are named to the user.
var Types = []string{TypeOpenAI, TypeAnthropic, TypeDeepSeek, TypeMuse}

// TypesText names the provider types in a sentence: "openai", "anthropic", "deepseek" or "muse".
func TypesText() string {
	out := ""
	for i, t := range Types {
		switch {
		case i == 0:
		case i == len(Types)-1:
			out += " or "
		default:
			out += ", "
		}
		out += fmt.Sprintf("%q", t)
	}
	return out
}

// Reasons says whether a provider type's models think before they answer, with an effort that can be set.
func Reasons(typ string) bool { return typ == TypeDeepSeek || typ == TypeMuse }

// Spec says how to reach a model API.
type Spec struct {
	Type      string // TypeOpenAI (default) or TypeAnthropic
	BaseURL   string // default depends on Type
	APIKey    string
	MaxTokens int // Anthropic only: per-reply limit
	// NoVision says the model cannot see images. They are then left out of what
	// is sent, with a note in their place, instead of the API refusing the request.
	NoVision bool
	// Thinking is DeepSeek's setting: "" (the service's default, on), "enabled" or "disabled".
	// ReasoningEffort is "" (the service's default) or a level the service knows: "low", "high" or
	// "max" for DeepSeek; "minimal", "low", "medium", "high", "xhigh" or "max" for Muse.
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
	case TypeMuse:
		return "https://api.meta.ai/v1"
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
	case TypeMuse:
		// Chat Completions on Meta Model API is the OpenAI shape. The Muse models always reason, and keep that
		// reasoning to themselves: there is nothing to send back, only how hard to think.
		o := NewOpenAI(base, s.APIKey)
		o.NoVision = s.NoVision
		o.ReasoningEffort = s.ReasoningEffort
		return o, nil
	case TypeAnthropic:
		a := NewAnthropic(base, s.APIKey, s.MaxTokens)
		a.NoVision = s.NoVision
		return a, nil
	}
	return nil, fmt.Errorf("unknown provider type %q (use %s)", s.Type, TypesText())
}
