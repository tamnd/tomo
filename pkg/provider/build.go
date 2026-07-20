package provider

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/tamnd/tomo/pkg/config"
)

// Build turns a config entry into a live Provider.
func Build(pc config.Provider) (Provider, error) {
	switch pc.Type {
	case "anthropic":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("anthropic provider: api_key is empty (is the env var set?)")
		}
		if pc.BaseURL != "" {
			if err := validateBaseURL(pc.BaseURL); err != nil {
				return nil, fmt.Errorf("anthropic provider: %w", err)
			}
		}
		return &Anthropic{APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
	case "openai":
		if pc.BaseURL == "" {
			return nil, fmt.Errorf("openai provider: base_url is required (e.g. https://api.openai.com/v1)")
		}
		if err := validateBaseURL(pc.BaseURL); err != nil {
			return nil, fmt.Errorf("openai provider: %w", err)
		}
		return &OpenAI{APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
	default:
		return nil, fmt.Errorf("provider type %q: want anthropic or openai", pc.Type)
	}
}

// validateBaseURL keeps credentials out of configuration URLs and limits plaintext provider traffic to explicitly local destinations.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base_url %q must be an absolute URL with a host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must not contain a query or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if localHTTPHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("plain HTTP base_url is allowed only for loopback or LAN providers")
	default:
		return fmt.Errorf("base_url scheme %q is unsupported; use https or local http", u.Scheme)
	}
}

func localHTTPHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
