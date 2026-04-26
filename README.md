# Encore

A local proxy service for free AI APIs that gracefully handles rate limiting with automatic wait-and-retry.

## What It Does

Free AI API platforms (like NVIDIA NIM) often impose aggressive rate limits. When you're using these APIs for real work, hitting a 429 means your tool or agent gets interrupted.

Encore sits between your application and the upstream API. When a request gets rate-limited, Encore waits and retries automatically — your application sees a normal, successful response as if nothing happened.

## Quick Start

### 1. Build

```bash
go build -o encore ./cmd/encore
```

### 2. Configure

Create `~/.config/encore/config.json` (see `config.example.json` for a full example):

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 9090
  },
  "log": {
    "consoleLevel": "info",
    "fileLevel": "debug"
  },
  "retry": {
    "maxRetries": 5,
    "retryInterval": 3000
  },
  "activeProviders": {
    "openai": "nvidia-nim",
    "anthropic": ""
  },
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

### 3. Run

```bash
./encore start
```

### 4. Point your app to Encore

Both protocols work simultaneously on the same port — Encore routes requests by URL path.

#### Zed — AI Panel

In Zed's `settings.json`, add an OpenAI-compatible provider:

```json
{
  "language_models": {
    "openai_compatible": {
      "Encore": {
        "api_url": "http://127.0.0.1:9090/v1",
        "available_models": [
          {
            "name": "deepseek-ai/deepseek-v3.2",
            "display_name": "DeepSeek V3.2",
            "max_tokens": 65536
          }
        ]
      }
    }
  }
}
```

Then select the model in the AI panel dropdown.

#### Claude Code

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:9090
```

#### Other OpenAI-compatible clients

Set API base URL to `http://127.0.0.1:9090/v1`.

For all cases above, remove the API key from your app config — Encore handles authentication.

## Features

- **Automatic retry** on 429 / 502 / 503 / 504, network errors, and masked errors (HTTP 200 with error body)
- **Dual-protocol support** — OpenAI and Anthropic (Claude Code) run simultaneously on the same port
- **Streaming support** — SSE responses are flushed in real-time
- **Multiple providers** — define as many as you want, activate one per protocol via `activeProviders`
- **Colored console logging** with independent file log level control
- **Zero external dependencies** — pure Go standard library

## Configuration Reference

All fields are required. Encore does not assume any defaults — if a field is missing, it tells you exactly what's wrong.

| Field | Description |
|---|---|
| `server.host` | Listen address (e.g. `127.0.0.1`) |
| `server.port` | Listen port (e.g. `9090`) |
| `log.consoleLevel` | Minimum console log level: `verbose` / `debug` / `info` / `error` |
| `log.fileLevel` | Minimum file log level (logs written to `~/Library/Logs/encore/`) |
| `retry.maxRetries` | Max retry attempts per request |
| `retry.retryInterval` | Delay between retries in milliseconds |
| `activeProviders.openai` | Key of the active OpenAI-protocol provider (empty string to disable) |
| `activeProviders.anthropic` | Key of the active Anthropic-protocol provider (empty string to disable) |
| `providers.*.name` | Display name |
| `providers.*.protocol` | `openai` or `anthropic` |
| `providers.*.baseUrl` | Upstream base URL (`openai`: include `/v1`; `anthropic`: no `/v1`) |
| `providers.*.apiKey` | API key for the upstream provider |

## License

MIT
