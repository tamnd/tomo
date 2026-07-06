# tomo demo. ascii-gif prepends the window chrome, theme, and framing;
# this tape carries only the action. The hidden block points HOME at a
# scratch dir so onboard writes cleanly and nothing touches your real config.
Set Width 1200
Set Height 800

Hide
Type "export HOME=/tmp/tomo-demo-home; rm -rf $HOME; mkdir -p $HOME" Enter
Type "export ANTHROPIC_API_KEY=sk-demo; export PS1='$ '; clear" Enter
Show

Type "# tomo: your personal AI agent, one Go binary" Sleep 600ms Enter
Type "tomo --help" Sleep 800ms Enter
Sleep 2.5s

Type "# Set up once: writes the config, memory, and skills under ~/.tomo" Sleep 600ms Enter
Type "tomo onboard" Sleep 800ms Enter
Sleep 2.5s

Type "# Model-agnostic: Anthropic native, or any OpenAI-compatible endpoint" Sleep 600ms Enter
Type "grep -A6 'providers:' ~/.tomo/config.yaml" Sleep 800ms Enter
Sleep 3s

Type "# Start the daemon: local web chat plus every channel, behind the policy gate" Sleep 600ms Enter
Type "tomo serve" Sleep 800ms Enter
Sleep 2.5s
Ctrl+C
Sleep 1s
