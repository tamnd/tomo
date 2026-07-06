---
title: "Installation"
description: "Install tomo from Go, a release archive, a Linux package, Homebrew or Scoop, or the container image, and what it needs to run."
weight: 20
---

tomo is a single static binary.
It is built `CGO_ENABLED=0`, so it links nothing and runs the same on any machine of its platform.
It needs a provider API key to talk to a model (for example `ANTHROPIC_API_KEY`), but it has no login and no account of its own: there is no tomo service to sign in to.

## Go

```bash
go install github.com/tamnd/tomo/cmd/tomo@latest
```

## Release archives and Linux packages

Every [release](https://github.com/tamnd/tomo/releases) attaches `tar.gz` archives (and a `.zip` for Windows) for Linux, macOS, Windows, and FreeBSD, plus `.deb`, `.rpm`, and `.apk` packages and a `checksums.txt`.
Download the one for your platform, extract `tomo`, and put it on your `PATH`.

```bash
# Debian/Ubuntu
sudo dpkg -i tomo_*_linux_amd64.deb

# Fedora/RHEL
sudo rpm -i tomo_*_linux_amd64.rpm
```

## Homebrew and Scoop

Homebrew and Scoop manifests publish alongside each release once their taps are configured, so a `brew install` and a `scoop install` land you the same binary on macOS and Windows.

## Container

The image carries the binary and nothing else.
tomo keeps its config and data under `$HOME/.tomo`, so mount a volume at the container's home and everything (the config, the sqlite ledger, memory, and the audit log) persists across runs:

```bash
docker run --rm -it \
  -e HOME=/data -v "$PWD/tomo-data:/data" \
  -e ANTHROPIC_API_KEY \
  -p 8765:8765 \
  ghcr.io/tamnd/tomo serve --addr 0.0.0.0:8765
```

The volume at `/data` holds `~/.tomo`, `-p 8765:8765` publishes the web chat, and binding `--addr 0.0.0.0:8765` makes it reachable from outside the container.
Run `onboard` once against the same volume first to write the starter config.

## What it needs to run

- A config at `~/.tomo/config.yaml`.
  Run `tomo onboard` to write a starter one (see the [quick start](/getting-started/quick-start/)).
- A provider key in your environment, referenced from the config with `${VAR}`.
  With the default config that is `ANTHROPIC_API_KEY`; point a provider at a local server instead and it can be whatever that server wants, or nothing.

## Optional external tools for voice

Voice is off until you configure it, and it leans on external binaries that tomo discovers on your `PATH` and never bundles:

- `whisper-cli` (whisper.cpp) transcribes inbound voice notes.
- `piper` speaks replies back.
- `ffmpeg` decodes inbound clips and encodes the spoken reply.

All of it runs locally, so no audio leaves the machine.
See the [voice guide](/guides/voice/) for the setup.

Next: [the quick start](/getting-started/quick-start/).
