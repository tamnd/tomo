package provider

import (
	"fmt"

	"github.com/tamnd/tomo/pkg/config"
)

// Build turns a config entry into a live Provider.
func Build(pc config.Provider) (Provider, error) {
	switch pc.Type {
	case "anthropic":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("anthropic provider: api_key is empty (is the env var set?)")
		}
		return &Anthropic{APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
	case "openai":
		if pc.BaseURL == "" {
			return nil, fmt.Errorf("openai provider: base_url is required (e.g. https://api.openai.com/v1)")
		}
		return &OpenAI{APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
	default:
		return nil, fmt.Errorf("provider type %q: want anthropic or openai", pc.Type)
	}
}
