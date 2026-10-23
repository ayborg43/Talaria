package config

// Preset is a one-click starting point for a profile: fill-in values for the
// settings form, all still editable. Anything OpenAI-compatible connects
// through type "openai" with its own address.
type Preset struct {
	Label   string `json:"label"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	KeyEnv  string `json:"key_env"`
	Hint    string `json:"hint"`
	// NeedsKey is false when the endpoint answers without one (a local model).
	NeedsKey bool `json:"needs_key"`
}

// Presets are the providers the settings page offers. Models move fast: these
// are starting points, and the form keeps whatever the user types instead.
func Presets() []Preset {
	return []Preset{
		{Label: "OpenAI", Type: "openai", Model: "gpt-4o-mini", KeyEnv: "OPENAI_API_KEY", NeedsKey: true,
			Hint: "OpenAI's own API. Paste a key from platform.openai.com."},
		{Label: "Anthropic", Type: "anthropic", Model: "claude-sonnet-4-5", KeyEnv: "ANTHROPIC_API_KEY", NeedsKey: true,
			Hint: "Anthropic's own API. Paste a key from console.anthropic.com."},
		{Label: "DeepSeek", Type: "deepseek", Model: "deepseek-chat", KeyEnv: "DEEPSEEK_API_KEY", NeedsKey: true,
			Hint: "DeepSeek's own API, with its thinking settings. Paste a key from platform.deepseek.com."},
		{Label: "OpenRouter", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "anthropic/claude-sonnet-4", KeyEnv: "OPENROUTER_API_KEY", NeedsKey: true,
			Hint: "One key for many models. Set the model to any id from openrouter.ai/models."},
		{Label: "Ollama", Type: "openai", BaseURL: "http://localhost:11434/v1", Model: "llama3.1",
			Hint: "Models on your own machine: no key, no account. Install Ollama and pull a model first."},
		{Label: "Google Gemini", Type: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-2.0-flash", KeyEnv: "GEMINI_API_KEY", NeedsKey: true,
			Hint: "Google's models through their OpenAI-compatible endpoint. Paste a key from aistudio.google.com."},
		{Label: "Mistral", Type: "openai", BaseURL: "https://api.mistral.ai/v1", Model: "mistral-large-latest", KeyEnv: "MISTRAL_API_KEY", NeedsKey: true,
			Hint: "Mistral's own API. Paste a key from console.mistral.ai."},
		{Label: "xAI", Type: "openai", BaseURL: "https://api.x.ai/v1", Model: "grok-4", KeyEnv: "XAI_API_KEY", NeedsKey: true,
			Hint: "xAI's own API. Paste a key from console.x.ai."},
	}
}
