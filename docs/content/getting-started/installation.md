---
title: "Installation"
description: "Install ari from Go, Homebrew, Scoop, a release archive, a Linux package, or the container image, and set your API key as an environment reference."
weight: 20
---

ari is a single binary. Pick whichever channel suits you, then set your API key in the environment.

## Go

```bash
go install github.com/tamnd/ari@latest
```

## Homebrew (macOS)

```bash
brew install tamnd/tap/ari
```

## Scoop (Windows)

```bash
scoop bucket add tamnd https://github.com/tamnd/scoop-bucket
scoop install ari
```

## Linux (apt and dnf)

A signed apt and dnf repository tracks every release, so `apt upgrade` and `dnf upgrade` keep ari current.

```bash
# Debian, Ubuntu
curl -fsSL https://tamnd.github.io/linux-repo/gpg.key \
  | sudo gpg --dearmor -o /usr/share/keyrings/tamnd.gpg
echo "deb [signed-by=/usr/share/keyrings/tamnd.gpg] https://tamnd.github.io/linux-repo/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/tamnd.list
sudo apt update && sudo apt install ari

# Fedora, RHEL
sudo dnf config-manager --add-repo https://tamnd.github.io/linux-repo/dnf/tamnd.repo
sudo dnf install ari
```

## Release archives and Linux packages

Every [release](https://github.com/tamnd/ari/releases) attaches `tar.gz` archives (and a `.zip` for Windows) for Linux, macOS, Windows, and FreeBSD, plus `.deb`, `.rpm`, and `.apk` packages. Download the one for your platform, extract `ari`, and put it on your `PATH`. To install a package directly without the repo above:

```bash
# Debian, Ubuntu
sudo dpkg -i ari_*_amd64.deb

# Fedora, RHEL
sudo rpm -i ari-*.x86_64.rpm
```

## Container

```bash
docker run --rm -v "$PWD:/work" -w /work \
  -e ANTHROPIC_API_KEY ghcr.io/tamnd/ari -p "summarize main.go"
```

The image ships from GHCR and reads the same environment references as the binary.

## Set your API key

ari never stores a key. It reads one from the environment at startup, so export the variable your provider needs before you run:

```bash
export ANTHROPIC_API_KEY=...   # Anthropic
export OPENAI_API_KEY=...       # OpenAI
export OPENROUTER_API_KEY=...   # OpenRouter
```

The config ari writes on first run holds only the reference `${ANTHROPIC_API_KEY}`, never the value. If you ever paste a literal key into a config file, `ari doctor` reports it as a critical finding. See the [config reference](/reference/configuration/) for the full surface and precedence.

Next: [the quick start](/getting-started/quick-start/).
