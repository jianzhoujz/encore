package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jianzhoujz/encore/internal/config"
	"github.com/jianzhoujz/encore/internal/logger"
)

// Server is the local reverse-proxy that forwards requests to the current
// upstream provider and retries automatically on rate-limit or transient errors.
type Server struct {
	config *config.Config
	logger *logger.Logger
	client *http.Client
}

// NewServer creates a proxy Server with sensible transport defaults.
func NewServer(cfg *config.Config, log *logger.Logger) *Server {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Server{
		config: cfg,
		logger: log,
		client: &http.Client{
			// No overall timeout — streaming responses may take arbitrarily long.
			Transport: transport,
		},
	}
}

// Start begins listening and serving on the configured address.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port)
	ap := s.config.ActiveProviders

	s.logger.Info("Starting Encore proxy server on %s", addr)
	s.logger.Info("Retry policy: max %d retries, %dms interval",
		s.config.Retry.MaxRetries, s.config.Retry.RetryInterval)

	if ap.OpenAI != "" {
		p := s.config.Providers[ap.OpenAI]
		s.logger.Info("OpenAI provider: %s (%s) -> %s", p.Name, ap.OpenAI, p.BaseURL)
	} else {
		s.logger.Info("OpenAI provider: (disabled)")
	}

	if ap.Anthropic != "" {
		p := s.config.Providers[ap.Anthropic]
		s.logger.Info("Anthropic provider: %s (%s) -> %s", p.Name, ap.Anthropic, p.BaseURL)
	} else {
		s.logger.Info("Anthropic provider: (disabled)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	return http.ListenAndServe(addr, mux)
}

// Anthropic API paths.
var anthropicPaths = map[string]bool{
	"/v1/messages": true,
}

// resolveProvider determines which provider should handle the request based on
// the URL path. Returns the provider config and true, or an empty config and
// false if no matching provider is active.
func (s *Server) resolveProvider(path string) (config.ProviderConfig, bool) {
	cleanPath := strings.TrimSuffix(path, "/")
	ap := s.config.ActiveProviders

	if anthropicPaths[cleanPath] {
		if ap.Anthropic != "" {
			return s.config.Providers[ap.Anthropic], true
		}
		return config.ProviderConfig{}, false
	}

	// Everything else is treated as OpenAI.
	if ap.OpenAI != "" {
		return s.config.Providers[ap.OpenAI], true
	}
	return config.ProviderConfig{}, false
}

// handleRequest dispatches incoming requests to the appropriate handler.
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Health-check probe (e.g. Claude Code sends HEAD / before connecting).
	if r.URL.Path == "/" {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	provider, ok := s.resolveProvider(r.URL.Path)
	if !ok {
		s.logger.Error("No active provider for path: %s", r.URL.Path)
		http.Error(w, "no active provider for this protocol", http.StatusBadGateway)
		return
	}

	s.proxyWithRetry(w, r, provider)
}

// proxyWithRetry forwards a request to the upstream provider, automatically
// retrying on 429 / 502 / 503 / 504 or network errors.
func (s *Server) proxyWithRetry(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig) {
	// Buffer the body so we can replay it on retries.
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			s.logger.Error("Failed to read request body: %s", err)
			http.Error(w, "failed to read request body", http.StatusInternalServerError)
			return
		}
	}

	s.logger.Info("-> %s %s", r.Method, r.URL.Path)

	upstreamURL := buildUpstreamURL(provider, r.URL.Path, r.URL.RawQuery)
	s.logger.Info("   Upstream: %s", upstreamURL)
	if len(bodyBytes) > 0 {
		s.logger.Debug("   Request body: %s", truncateBody(bodyBytes, 1024))
	}

	headers := buildUpstreamHeaders(r.Header, provider)

	resp, err := s.doWithRetry(r.Context(), r.Method, upstreamURL, bodyBytes, headers)
	if err != nil {
		s.logger.Error("Upstream request failed: %s", err)
		http.Error(w, "upstream request failed after retries", http.StatusBadGateway)
		return
	}

	s.logger.Info("<- %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	pipeResponse(w, resp)
}

// ---------------------------------------------------------------------------
// Retry core
// ---------------------------------------------------------------------------

// doWithRetry performs an HTTP request with automatic retry on retryable status
// codes (429, 502, 503, 504) and network errors. On the last attempt it returns
// whatever response the server sent, even if the status is retryable.
func (s *Server) doWithRetry(ctx context.Context, method, url string, body []byte, headers http.Header) (*http.Response, error) {
	maxRetries := s.config.Retry.MaxRetries
	retryInterval := time.Duration(s.config.Retry.RetryInterval) * time.Millisecond

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			s.logger.Info("   Retrying in %v (attempt %d/%d)...", retryInterval, attempt, maxRetries)
			time.Sleep(retryInterval)
		}

		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		for key, values := range headers {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			s.logger.Error("Request failed: %s", err)
			if attempt < maxRetries {
				continue
			}
			return nil, fmt.Errorf("request failed after %d retries: %w", maxRetries, lastErr)
		}

		if isRetryable(resp.StatusCode) && attempt < maxRetries {
			s.logger.Info("   <- %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		// Check for errors masquerading as HTTP 200 (e.g. NVIDIA NIM returns
		// "rate limit exceeded" or "gateway timeout" as plain-text 200 responses).
		if resp.StatusCode == http.StatusOK && attempt < maxRetries {
			if retryable, errMsg := checkForMaskedError(resp); retryable {
				s.logger.Info("   <- 200 OK (masked error: %s)", truncateBody([]byte(errMsg), 200))
				resp.Body.Close()
				lastErr = fmt.Errorf("masked error: %s", errMsg)
				continue
			}
		}

		return resp, nil
	}

	return nil, lastErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildUpstreamURL constructs the full upstream URL.
// For OpenAI protocol, strips the /v1 prefix (because baseUrl already includes it).
// For Anthropic protocol, appends the path as-is (baseUrl does not include /v1).
func buildUpstreamURL(provider config.ProviderConfig, path, rawQuery string) string {
	var upstreamPath string
	if provider.Protocol == "openai" {
		upstreamPath = strings.TrimPrefix(path, "/v1")
	} else {
		upstreamPath = path
	}
	u := strings.TrimSuffix(provider.BaseURL, "/") + upstreamPath
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// buildUpstreamHeaders creates a header set suitable for the upstream request
// by copying the client headers (minus hop-by-hop and auth) and setting the
// correct auth header based on protocol.
func buildUpstreamHeaders(src http.Header, provider config.ProviderConfig) http.Header {
	skip := map[string]bool{
		"Connection":        true,
		"Keep-Alive":        true,
		"Transfer-Encoding": true,
		"Upgrade":           true,
		"Authorization":     true,
		"X-Api-Key":         true,
	}
	headers := make(http.Header)
	for key, values := range src {
		if skip[key] {
			continue
		}
		for _, v := range values {
			headers.Add(key, v)
		}
	}
	if provider.Protocol == "anthropic" {
		headers.Set("x-api-key", provider.APIKey)
	} else {
		headers.Set("Authorization", "Bearer "+provider.APIKey)
	}
	return headers
}

// pipeResponse writes the upstream HTTP response back to the client, handling
// both regular and streaming (SSE) responses.
func pipeResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		// Streaming — flush each chunk immediately.
		flusher, ok := w.(http.Flusher)
		if !ok {
			io.Copy(w, resp.Body)
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		io.Copy(w, resp.Body)
	}
}

// isRetryable returns true for HTTP status codes that warrant a retry.
func isRetryable(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// truncateBody returns the body as a string, truncated to maxLen bytes with
// an ellipsis suffix if it exceeds that length. Used for debug logging.
func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "...(truncated)"
}

// ---------------------------------------------------------------------------
// Masked-error detection (NVIDIA NIM workaround)
// ---------------------------------------------------------------------------
//
// Some providers (notably NVIDIA NIM) return errors — including rate-limit and
// gateway failures — as HTTP 200 with an error message in the body instead of
// using proper 4xx/5xx status codes. We detect these by inspecting the body of
// short, non-streaming 200 responses.

// retryableBodyPatterns are substrings that, when found in a short HTTP 200
// response body, indicate the upstream returned a retryable error.
var retryableBodyPatterns = []string{
	"rate limit exceeded",
	"too many requests",
	"upstream connect error",
	"gateway timeout",
	"internal server error",
	"service unavailable",
	"server busy",
}

// maxMaskedErrorBodySize is the maximum response body size (in bytes) we will
// inspect for masked errors. Real AI completions are typically much larger, so
// a small cap avoids false positives and keeps the check lightweight.
const maxMaskedErrorBodySize = 1024

// checkForMaskedError reads the body of an HTTP 200 non-streaming response and
// checks whether it actually contains an error message. If so, it returns
// retryable=true and the error text. The response body is always replaced with
// a rewound reader so the caller can still use it (for logging or piping).
func checkForMaskedError(resp *http.Response) (retryable bool, errMsg string) {
	// Only inspect non-streaming 200s.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return false, ""
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxMaskedErrorBodySize+1))
	remaining, _ := io.ReadAll(resp.Body) // drain any remainder
	resp.Body.Close()

	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return false, ""
	}

	// If the body is larger than our cap, it's almost certainly a real
	// response — reassemble and return.
	if len(bodyBytes) > maxMaskedErrorBodySize {
		full := append(bodyBytes, remaining...)
		resp.Body = io.NopCloser(bytes.NewReader(full))
		return false, ""
	}

	// Restore body for the caller.
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Strategy 1: JSON with a top-level "error" key (and no "choices").
	var obj map[string]json.RawMessage
	if json.Unmarshal(bodyBytes, &obj) == nil {
		if _, hasChoices := obj["choices"]; hasChoices {
			return false, "" // legitimate completion response
		}
		if errRaw, hasErr := obj["error"]; hasErr {
			var errObj map[string]interface{}
			if json.Unmarshal(errRaw, &errObj) == nil {
				if msg, ok := errObj["message"].(string); ok && isRetryableMessage(msg) {
					return true, msg
				}
			}
			// "error" might be a plain string
			var errStr string
			if json.Unmarshal(errRaw, &errStr) == nil && isRetryableMessage(errStr) {
				return true, errStr
			}
		}
	}

	// Strategy 2: short plain-text body with a known error pattern.
	bodyStr := strings.TrimSpace(string(bodyBytes))
	if len(bodyStr) <= 512 && isRetryableMessage(bodyStr) {
		return true, bodyStr
	}

	return false, ""
}

// isRetryableMessage returns true if the message contains any of the known
// retryable error patterns (case-insensitive).
func isRetryableMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, pat := range retryableBodyPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}
