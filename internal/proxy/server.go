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
	"sync"
	"time"

	"github.com/jianzhoujz/encore/internal/config"
	"github.com/jianzhoujz/encore/internal/logger"
)

// Server is the local reverse-proxy that forwards requests to a single
// upstream provider and retries automatically on rate-limit or transient errors.
type Server struct {
	provider config.ProviderConfig
	config   *config.Config
	logger   *logger.Logger
	client   *http.Client
}

// NewServer creates a proxy Server bound to a specific upstream provider.
func NewServer(cfg *config.Config, provider config.ProviderConfig, log *logger.Logger) *Server {
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
		provider: provider,
		config:   cfg,
		logger:   log,
		client: &http.Client{
			Transport: transport,
		},
	}
}

// Start begins listening and serving on the given address.
func (s *Server) Start(addr string) error {
	s.logger.Info("Starting Encore proxy server on %s (%s -> %s)",
		addr, s.provider.Name, s.provider.BaseURL)
	s.logger.Info("Retry policy: max %d retries, %dms interval",
		s.config.Retry.MaxRetries, s.config.Retry.RetryInterval)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	return http.ListenAndServe(addr, mux)
}

// handleRequest dispatches incoming requests to the configured provider.
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Health-check probe (e.g. Claude Code sends HEAD / before connecting).
	if r.URL.Path == "/" {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Custom model list override — if the configured provider has a models file,
	// serve it directly instead of proxying upstream.
	if s.handleModels(w, r) {
		return
	}

	s.proxyWithRetry(w, r)
}

// proxyWithRetry forwards a request to the upstream provider, automatically
// retrying on 429 / 502 / 503 / 504 or network errors.
func (s *Server) proxyWithRetry(w http.ResponseWriter, r *http.Request) {
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

	// Override model name if configured.
	bodyBytes = s.overrideModel(bodyBytes)

	upstreamURL := buildUpstreamURL(s.provider, r.URL.Path, r.URL.RawQuery)
	s.logger.Info("   Upstream: %s", upstreamURL)
	if len(bodyBytes) > 0 {
		s.logger.Debug("   Request body: %s", truncateBody(bodyBytes, 1024))
	}

	headers := buildUpstreamHeaders(r.Header, s.provider)

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
// StartServers starts both OpenAI and Anthropic servers based on config.
// ---------------------------------------------------------------------------

// StartServers starts all configured protocol servers.
func StartServers(cfg *config.Config, log *logger.Logger) error {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	start := func(protocol string, port int) {
		defer wg.Done()
		provider, ok := cfg.ActiveProvider(protocol)
		if !ok {
			log.Info("%s provider: (disabled)", protocol)
			return
		}
		srv := NewServer(cfg, provider, log)
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, port)
		if err := srv.Start(addr); err != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
		}
	}

	if port := cfg.Server.OpenaiPort; port > 0 {
		wg.Add(1)
		go start("openai", port)
	}
	if port := cfg.Server.AnthropicPort; port > 0 {
		wg.Add(1)
		go start("anthropic", port)
	}

	wg.Wait()
	return firstErr
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

		// Check for rate-limit / server errors disguised as HTTP 400 (IdeaLab).
		// Reads the body to detect retryable patterns; if not retryable, the
		// response is passed through to the client unchanged.
		if resp.StatusCode == http.StatusBadRequest && attempt < maxRetries {
			if retryable, errMsg := checkFor400Error(resp); retryable {
				s.logger.Info("   <- 400 Bad Request (retryable: %s)", truncateBody([]byte(errMsg), 200))
				resp.Body.Close()
				lastErr = fmt.Errorf("400 retryable: %s", errMsg)
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

// overrideModel rewrites the "model" field in the request body JSON to the
// configured OverrideModel value. If OverrideModel is empty or the body is not
// valid JSON, the body is returned unchanged.
func (s *Server) overrideModel(body []byte) []byte {
	if s.provider.OverrideModel == "" || len(body) == 0 {
		return body
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}

	modelVal, err := json.Marshal(s.provider.OverrideModel)
	if err != nil {
		return body
	}
	obj["model"] = modelVal

	rewritten, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return rewritten
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
	"模型提供方限流",
	"限流",
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

// checkFor400Error reads the body of an HTTP 400 response and determines
// whether it's a retryable error (e.g. rate-limit from IdeaLab disguised as
// 400). Non-retryable 400s (bad request, invalid model, etc.) are returned
// as-is. The response body is always restored for the caller.
func checkFor400Error(resp *http.Response) (retryable bool, errMsg string) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return false, ""
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxMaskedErrorBodySize+1))
	remaining, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return false, ""
	}

	if len(bodyBytes) > maxMaskedErrorBodySize {
		full := append(bodyBytes, remaining...)
		resp.Body = io.NopCloser(bytes.NewReader(full))
		return false, ""
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Check JSON error bodies for retryable patterns.
	var obj map[string]json.RawMessage
	if json.Unmarshal(bodyBytes, &obj) == nil {
		if errRaw, hasErr := obj["error"]; hasErr {
			var errObj map[string]interface{}
			if json.Unmarshal(errRaw, &errObj) == nil {
				if msg, ok := errObj["message"].(string); ok && isRetryableMessage(msg) {
					return true, msg
				}
				// Some APIs use "code" or "type" fields for error classification.
				if code, ok := errObj["code"].(string); ok && isRetryableMessage(code) {
					return true, code
				}
			}
			var errStr string
			if json.Unmarshal(errRaw, &errStr) == nil && isRetryableMessage(errStr) {
				return true, errStr
			}
		}
		// Also check top-level "message" or "code" fields.
		for _, key := range []string{"message", "code", "msg", "error_message"} {
			if raw, ok := obj[key]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil && isRetryableMessage(s) {
					return true, s
				}
			}
		}
	}

	// Check plain-text body.
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
