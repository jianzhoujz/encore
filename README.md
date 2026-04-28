<p align="center">
  <h1 align="center">Encore</h1>
  <p align="center">
    <strong>Stop losing requests to rate limits. Use free AI APIs like a pro.</strong>
  </p>
  <p align="center">
    <a href="https://github.com/jianzhoujz/encore/releases"><img src="https://img.shields.io/github/v/release/jianzhoujz/encore?style=flat-square&color=blue" alt="Release"></a>
    <a href="https://github.com/jianzhoujz/encore/blob/main/LICENSE"><img src="https://img.shields.io/github/license/jianzhoujz/encore?style=flat-square" alt="License"></a>
    <a href="https://github.com/jianzhoujz/encore"><img src="https://img.shields.io/badge/platform-macOS-lightgrey?style=flat-square" alt="Platform"></a>
    <a href="https://github.com/jianzhoujz/encore"><img src="https://img.shields.io/badge/dependencies-zero-brightgreen?style=flat-square" alt="Dependencies"></a>
  </p>
  <p align="center">
    <a href="./README_CN.md">中文文档</a>
  </p>
</p>

---

## The Problem

Platforms like **NVIDIA NIM** give you free access to powerful AI models — DeepSeek, LLaMA, Mistral, and more. But free tiers come with aggressive rate limits. One burst of requests and you're hit with `429 Too Many Requests`, breaking your workflow, crashing your AI agent, or stalling your IDE assistant mid-thought.

You shouldn't have to babysit API calls. Your tools should just work.

## The Solution

**Encore** is a lightweight local proxy that sits between your application and the upstream AI API. When a request gets rate-limited (or hits a transient server error), Encore absorbs the failure, waits, and retries — automatically. Your application sees a clean, successful response every time, as if the rate limit never existed.

```
┌─────────────┐         ┌─────────────┐         ┌──────────────┐
│  Your App   │ ──────> │    Encore   │ ──────> │  NVIDIA NIM  │
│  (Claude    │         │  :9090 /    │  429?   │  or any API  │
│   Code, etc)│ <────── │   :9091     │  retry! │              │
└─────────────┘   200 OK│  OpenAI /   │ <────── │              │
                        │  Anthropic  │         └──────────────┘
                        └─────────────┘
```

**Zero code changes required.** Just point your app's API base URL to the correct port — that's it.

## Install

```bash
brew tap jianzhoujz/tap
brew install encore
```

To upgrade:

```bash
brew update --force
brew upgrade encore
```

Or build from source:

```bash
go build -o encore ./cmd/encore
```

## Quick Start

**1. Create the config file** at `~/.config/encore/config.json`:

```json
{
  "server": { "host": "127.0.0.1", "openaiPort": 9090, "anthropicPort": 9091 },
  "log": { "consoleLevel": "info", "fileLevel": "debug" },
  "retry": { "maxRetries": 5, "retryInterval": 3000 },
  "activeProviders": { "openai": "nvidia-nim", "anthropic": "" },
  "providers": {
    "nvidia-nim": {
      "name": "NVIDIA NIM",
      "protocol": "openai",
      "baseUrl": "https://integrate.api.nvidia.com/v1",
      "apiKey": "nvapi-xxxxxxxxxxxx"
    }
  }
}
```

**2. Start the proxy:**

```bash
encore start
```

**3. Point your app to Encore** — just change the API base URL:

| Client | Port | Configuration |
|---|---|---|
| **Claude Code** | Anthropic (`:9091`) | `ANTHROPIC_BASE_URL=http://127.0.0.1:9091` |
| **Any OpenAI client** | OpenAI (`:9090`) | Set base URL to `http://127.0.0.1:9090/v1` |

Remove the API key from your app — Encore injects it automatically.

## Why Encore?

| | Without Encore | With Encore |
|---|---|---|
| Rate limit hit | App crashes or shows error | Transparent retry, app never knows |
| Server 502/503 | Request lost | Auto-retry with backoff |
| Masked errors (NIM) | Silent failure, wrong output | Detected and retried |
| API key management | Scattered across apps | Single config, one place |
| Protocol switching | Reconfigure everything | Separate ports, zero confusion |

## Features

- **Smart retry** — Handles 429, 502, 503, 504, network errors, and even [masked errors](#masked-error-detection) (NVIDIA NIM returning errors inside HTTP 200)
- **Dual-port** — Separate OpenAI (`openaiPort`) and Anthropic (`anthropicPort`) servers. Each port is bound to one protocol, no path guessing needed.
- **Custom model list** — Override the `/v1/models` response per provider with a local JSON file, so your clients see exactly the models you want
- **Model name override** — Force all requests to use a specific model name by overriding the `model` field in every request body
- **Real-time streaming** — SSE responses are flushed chunk-by-chunk, no buffering delay
- **Multiple providers** — Define as many upstreams as you want, activate one per protocol via `activeProviders`
- **Zero dependencies** — Pure Go standard library, single static binary
- **Homebrew ready** — `brew install` and go

## Client Configuration

### Claude Code

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:9091
```

### Other Clients

Any OpenAI-compatible tool — set the base URL to `http://127.0.0.1:9090/v1` and remove the API key.

## Masked Error Detection

Some providers (notably NVIDIA NIM) occasionally return errors disguised as HTTP 200 — the status code looks fine but the body says `"rate limit exceeded"`. Most retry logic misses this entirely.

Encore catches these by inspecting short non-streaming 200 responses for known error patterns: `rate limit exceeded`, `too many requests`, `upstream connect error`, `gateway timeout`, `service unavailable`, and more. When detected, the request is retried just like a real 429.

## Configuration Reference

Provider fields (`name`, `protocol`, `baseUrl`, `apiKey`) are required. `models` and `anthropicPort` are optional. Encore validates strictly and tells you exactly what's missing.

| Field | Description |
|---|---|
| `server.host` | Listen address (e.g. `127.0.0.1`) |
| `server.openaiPort` | OpenAI-protocol listen port (e.g. `9090`) |
| `server.anthropicPort` | Anthropic-protocol listen port (e.g. `9091`). `0` = disabled |
| `log.consoleLevel` | Console log level: `verbose` / `debug` / `info` / `error` |
| `log.fileLevel` | File log level (logs to `~/Library/Logs/encore/`) |
| `retry.maxRetries` | Max retry attempts per request |
| `retry.retryInterval` | Delay between retries (ms) |
| `activeProviders.openai` | Active OpenAI-protocol provider key (empty to disable) |
| `activeProviders.anthropic` | Active Anthropic-protocol provider key (empty to disable) |
| `providers.*.name` | Display name |
| `providers.*.protocol` | `openai` or `anthropic` |
| `providers.*.baseUrl` | Upstream base URL |
| `providers.*.apiKey` | Upstream API key |
| `providers.*.models` | (Optional) Custom model list JSON filename in config dir |
| `providers.*.overrideModel` | (Optional) Force all requests to use this model name, overriding the client's `model` field |

## Tested With

- **NVIDIA NIM** — DeepSeek V3.2, DeepSeek V4 series (free tier, ~40 RPM)
- **Claude Code** (Anthropic protocol)

## License

MIT
