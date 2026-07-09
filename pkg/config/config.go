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
	Sandbox      string              `yaml:"sandbox"`
	Channels     Channels            `yaml:"channels"`
	Heartbeat    Heartbeat           `yaml:"heartbeat"`
	Voice        Voice               `yaml:"voice"`
	MCP          MCP                 `yaml:"mcp"`
	Workers      map[string]Worker   `yaml:"workers"`
	DataDir      string              `yaml:"data_dir"`
	Workspace    string              `yaml:"workspace"`
}

// Worker is a named specialist that handles some conversations in its own
// right: its own persona, an optional model, its own policy, and the
// channel:chat bindings that route to it. Anything left unset falls back to the
// top-level default. The default worker, tomo, needs no entry here.
type Worker struct {
	Persona   string   `yaml:"persona"`   // extra system-prompt lines that set its role
	Model     string   `yaml:"model"`     // provider/model override, empty means the default
	Policy    Policy   `yaml:"policy"`    // its own gate, merged over the top-level policy
	Sandbox   string   `yaml:"sandbox"`   // exec sandbox for this worker, empty means the default
	Workspace string   `yaml:"workspace"` // working directory for this worker, empty means the default
	Channels  []string `yaml:"channels"`  // channel:chat keys whose messages route to it
}

// MCP lists the Model Context Protocol servers to attach on startup. Each one
// contributes its tools, namespaced by the server key.
type MCP struct {
	Servers map[string]MCPServer `yaml:"servers"`
}

// MCPServer describes one MCP server. Set command to launch it as a local
// subprocess over stdio, or url to reach a remote one over HTTP.
type MCPServer struct {
	Command string            `yaml:"command"` // executable to run for a stdio server
	Args    []string          `yaml:"args"`    // its arguments
	Env     map[string]string `yaml:"env"`     // extra environment for it
	URL     string            `yaml:"url"`     // endpoint of an HTTP server
	Headers map[string]string `yaml:"headers"` // sent on every HTTP request, for auth
}

// Voice configures speech both ways, all handled locally so no audio leaves the
// machine. Setting model turns on transcription of inbound voice notes with
// whisper.cpp; setting tts_model turns on spoken replies with piper, sent back
// as a voice note wherever the user spoke first.
type Voice struct {
	Model    string `yaml:"model"`     // path to a ggml whisper model; setting it enables voice-in
	Bin      string `yaml:"bin"`       // whisper.cpp cli, defaults to whisper-cli
	FFmpeg   string `yaml:"ffmpeg"`    // ffmpeg for decode and opus encode, defaults to ffmpeg
	TTSModel string `yaml:"tts_model"` // path to a piper voice model; setting it enables voice-out
	TTSBin   string `yaml:"tts_bin"`   // piper cli, defaults to piper
}

// Heartbeat runs tomo on a cadence against a checklist file, so it can pick up
// standing work without being spoken to. It stays quiet when there is nothing
// to report. Off unless enabled.
type Heartbeat struct {
	Enabled bool   `yaml:"enabled"`
	Every   string `yaml:"every"`   // schedule spec, defaults to @every 30m
	File    string `yaml:"file"`    // checklist to read each beat, defaults to HEARTBEAT.md in the data dir
	Channel string `yaml:"channel"` // where to deliver anything worth saying, defaults to web
	Chat    string `yaml:"chat"`    // chat id within that channel
}

// Channels configures the front doors serve turns on. It is a map from a
// channel name to that channel's own settings, left untyped on purpose: the
// config package does not know what a Telegram token or a Discord allow-list
// is, only the channel's driver does. To add a channel you register a driver
// and add a block here; the config schema never grows a field per channel.
//
//	channels:
//	  telegram:
//	    token: ${TELEGRAM_TOKEN}
//	    allow_chats: [123456789]
//	  discord:
//	    token: ${DISCORD_TOKEN}
//	    allow_channels: ["C0123"]
type Channels map[string]map[string]any

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
	c.Workspace = resolveWorkspace(c.Workspace)
	if c.Heartbeat.Enabled {
		if c.Heartbeat.Every == "" {
			c.Heartbeat.Every = "@every 30m"
		}
		if c.Heartbeat.File == "" {
			c.Heartbeat.File = filepath.Join(c.DataDir, "HEARTBEAT.md")
		}
		if c.Heartbeat.Channel == "" {
			c.Heartbeat.Channel = "web"
		}
	}
}

// resolveWorkspace turns a configured workspace into an absolute path the tools
// can anchor to. An empty value means the directory tomo was launched from,
// which keeps the old behavior where a relative path resolved against the
// process cwd. A leading ~ expands to the home dir. A path that cannot be made
// absolute is left as given rather than failing the whole load.
func resolveWorkspace(ws string) string {
	ws = strings.TrimSpace(ws)
	if ws == "~" || strings.HasPrefix(ws, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ws[1:])
		}
	}
	if ws == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "."
	}
	if abs, err := filepath.Abs(ws); err == nil {
		return abs
	}
	return ws
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
