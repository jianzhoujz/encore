## Development Guide

This document covers the internal architecture, conventions, and build instructions for Encore. For user-facing documentation, see `README.md`.

### Project Structure

```
encore/
├── cmd/encore/main.go          # CLI entry point
├── internal/
│   ├── config/config.go        # Config loading & strict validation
│   ├── logger/logger.go        # Colored console + file logging
│   └── proxy/
│       └── server.go           # HTTP reverse proxy with retry
├── docs/DEVELOPMENT.md         # This file
├── config.example.json         # Example configuration
├── go.mod
└── .gitignore
```

### Architecture Overview

Encore is a single-process HTTP reverse proxy that supports two API protocols simultaneously on the same port: OpenAI-compatible and Anthropic (Claude Code). On startup it reads a JSON config file, initializes a dual-output logger, and starts an `http.Server`.

The `activeProviders` config maps each protocol to one provider. Incoming requests are routed by URL path: `/v1/messages` goes to the Anthropic provider, all other `/v1/...` paths go to the OpenAI provider. Either slot can be left empty to disable that protocol. The routing logic lives in `resolveProvider`, which inspects the path and returns the matching provider.

The core loop lives in `proxyWithRetry`: it buffers the client request body, sends it to the upstream URL, and transparently retries on 429 (rate-limited), 502, 503, 504, network errors, or masked errors (HTTP 200 with error body). The retry count and interval are user-configurable. Streaming responses (`text/event-stream`) are flushed chunk-by-chunk via `http.Flusher`.

### Config

Path: `~/.config/encore/config.json`

The config loader performs two-phase validation:

1. **Structural** — parse the raw JSON into `map[string]json.RawMessage` and verify that every required key is physically present at every nesting level. This catches missing fields before Go's zero-value semantics silently swallow them.
2. **Semantic** — after unmarshalling into typed structs, validate value constraints (port range, valid log levels, `activeProviders` references exist and match protocol, etc.).

All fields are mandatory; the program refuses to fill in any defaults. If any field is missing or invalid, the program exits with a comprehensive list of all errors at once.

### Logging

The logger is a lightweight custom implementation (no third-party dependency) that writes to two destinations simultaneously:

| Destination | Format | Color |
|---|---|---|
| Console (stdout) | `YYYY-MM-DD HH:MM:SS [LEVEL] message` | Yes |
| File (`~/Library/Logs/encore/encore.log`) | `YYYY-MM-DD HH:MM:SS [LEVEL] message` | No |

Each destination has its own configurable minimum level. Levels from lowest to highest: `verbose` < `debug` < `info` < `error`.

Console color scheme: VERBOSE = gray, DEBUG = cyan, INFO = green, ERROR = red.

### URL Routing

The local proxy listens on the configured `host:port`. Both protocols coexist on the same port — routing is determined by URL path.

**Path-based protocol resolution (`resolveProvider`):**

- `/v1/messages` → Anthropic provider (from `activeProviders.anthropic`)
- All other paths → OpenAI provider (from `activeProviders.openai`)

If the target protocol's provider is not configured (empty string in `activeProviders`), the request receives a 502 error.

**OpenAI protocol (`"openai"`):** The upstream URL is constructed as `baseUrl + path.TrimPrefix("/v1")`. For example, with `baseUrl = "https://integrate.api.nvidia.com/v1"` and path `/v1/chat/completions`, the upstream becomes `https://integrate.api.nvidia.com/v1/chat/completions`. Auth header: `Authorization: Bearer <apiKey>`.

**Anthropic protocol (`"anthropic"`):** The path is appended as-is (no stripping), so the upstream becomes `baseUrl + /v1/messages`. Auth header: `x-api-key: <apiKey>`.

### Known Upstream: NVIDIA NIM

NVIDIA NIM exposes an OpenAI-compatible API at `https://integrate.api.nvidia.com/v1`.

**Rate limiting:** Default limit is ~40 RPM for free-tier accounts. When exceeded, NIM returns standard HTTP 429. However, under certain load conditions, NIM is known to return error messages (e.g. `"rate limit exceeded"`, `"upstream connect error"`) inside HTTP 200 response bodies — this is the reason for the masked-error detection logic described in the Retry Logic section.

### Retry Logic

Retryable conditions:

- HTTP 429 Too Many Requests
- HTTP 502 Bad Gateway
- HTTP 503 Service Unavailable
- HTTP 504 Gateway Timeout
- Network-level errors (connection refused, DNS failure, timeout, etc.)
- **Masked errors** — HTTP 200 responses whose body contains a known error message instead of a real completion (see below)

All other HTTP status codes are passed through to the client immediately.

When a retryable condition is encountered, the proxy sleeps for `retryInterval` milliseconds and then replays the exact same request. After `maxRetries` consecutive failures, the last error or status code is returned to the client.

#### Masked-Error Detection (NVIDIA NIM workaround)

Some upstream providers — most notably NVIDIA NIM — occasionally return errors disguised as HTTP 200 responses: the body contains an error message string rather than a valid completion. This is non-compliant with the OpenAI API specification but is a confirmed behavior documented in the NVIDIA developer forums.

Encore detects these by inspecting non-streaming 200 responses whose body is ≤ 1024 bytes. Two strategies are applied:

1. **JSON with `error` key (no `choices`)** — If the body parses as JSON containing an `error` field but no `choices`, and the error message matches a retryable pattern, the response is treated as retryable.
2. **Short plain text** — If the body is ≤ 512 bytes of plain text matching a retryable pattern, it is treated as retryable.

Retryable patterns (case-insensitive substring match): `rate limit exceeded`, `too many requests`, `upstream connect error`, `gateway timeout`, `internal server error`, `service unavailable`, `server busy`.

Responses with `choices` in the JSON body are never treated as masked errors regardless of content, and streaming responses (`text/event-stream`) bypass this check entirely.

### Terminal Notifications

On every retry and on final retry exhaustion, Encore writes an iTerm2-style OSC 9 desktop-notification sequence to `/dev/tty`. This mirrors Claude Code's terminal-native notification behavior: terminals that support OSC 9 (including iTerm2, Ghostty, Kitty, WezTerm, Warp, and Rio) may surface it as a desktop notification, while unsupported terminals ignore it. Encore does not fall back to hooks, AppleScript, or any external notification command.

### Build & Run

```bash
# Build
go build -o encore ./cmd/encore

# Run
./encore start

# Show version
./encore version
```

### Dependencies

Zero external dependencies. The entire project uses only Go's standard library.
