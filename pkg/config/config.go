// Package config loads ~/.tomo/config.yaml: providers, the default model,
// and agent knobs. Values may reference environment variables with ${VAR} so
// keys never have to live in the file itself.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider is one model backend entry.
type Provider struct {
	Type    string `yaml:"type"` // "anthropic" or "openai"
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

// Agent holds the loop knobs.
type Agent struct {
	MaxTokens int `yaml:"max_tokens"`
	MaxTurns  int `yaml:"max_turns"`
}

// Config is the whole file. Policy is left as a raw map so pkg/config need
// not import pkg/policy; the cli decodes it into policy.Config.
type Config struct {
	DefaultModel string              `yaml:"default_model"`
	Providers    map[string]Provider `yaml:"providers"`
	Agent        Agent               `yaml:"agent"`
	Policy       Policy              `yaml:"policy"`
	Channels     Channels            `yaml:"channels"`
	DataDir      string              `yaml:"data_dir"`
}

// Channels configures the front doors serve turns on.
type Channels struct {
	Telegram Telegram `yaml:"telegram"`
	Discord  Discord  `yaml:"discord"`
	Slack    Slack    `yaml:"slack"`
	IMessage IMessage `yaml:"imessage"`
}

// Telegram holds the bot token and the chats allowed to reach it.
type Telegram struct {
	Token      string  `yaml:"token"`
	AllowChats []int64 `yaml:"allow_chats"`
}

// Discord holds the bot token and the channel ids allowed to reach it.
type Discord struct {
	Token         string   `yaml:"token"`
	AllowChannels []string `yaml:"allow_channels"`
}

// Slack holds the app-level and bot tokens and the channels allowed to reach
// the bot. The app token opens the socket; the bot token posts messages.
type Slack struct {
	AppToken      string   `yaml:"app_token"`
	BotToken      string   `yaml:"bot_token"`
	AllowChannels []string `yaml:"allow_channels"`
}

// IMessage configures the macOS iMessage channel. It is off unless enabled,
// since it reaches a real Messages account. AllowHandles lists the phone
// numbers or emails permitted to drive the agent.
type IMessage struct {
	Enabled      bool     `yaml:"enabled"`
	AllowHandles []string `yaml:"allow_handles"`
	DBPath       string   `yaml:"db_path"`
}

// Policy mirrors the policy section without depending on pkg/policy.
type Policy struct {
	Read  string            `yaml:"read"`
	Net   string            `yaml:"net"`
	Write string            `yaml:"write"`
	Exec  string            `yaml:"exec"`
	Rules map[string]string `yaml:"rules"`
}

// DefaultPath returns ~/.tomo/config.yaml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tomo", "config.yaml"), nil
}

// Load reads and expands the config at path; an empty path means the default
// location. A missing file is an error that names the fix.
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no config at %s (run: tomo onboard)", path)
		}
		return nil, err
	}
	expanded := os.Expand(string(raw), func(key string) string {
		return os.Getenv(key)
	})
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Agent.MaxTokens == 0 {
		c.Agent.MaxTokens = 8192
	}
	if c.Agent.MaxTurns == 0 {
		c.Agent.MaxTurns = 24
	}
	if c.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.DataDir = filepath.Join(home, ".tomo")
		}
	}
}

// Resolve splits a provider/model spec against the configured providers. An
// empty spec falls back to default_model. The model part may itself contain
// slashes, which some gateways use.
func (c *Config) Resolve(spec string) (name, model string, pc Provider, err error) {
	if spec == "" {
		spec = c.DefaultModel
	}
	if spec == "" {
		return "", "", Provider{}, fmt.Errorf("no model given and no default_model in config")
	}
	name, model, ok := strings.Cut(spec, "/")
	if !ok || name == "" || model == "" {
		return "", "", Provider{}, fmt.Errorf("model %q: want provider/model, like anthropic/claude-fable-5", spec)
	}
	pc, ok = c.Providers[name]
	if !ok {
		return "", "", Provider{}, fmt.Errorf("model %q: no provider %q in config", spec, name)
	}
	return name, model, pc, nil
}
