package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestCheckFor400ErrorDetectsNestedDetailMessageThrottle(t *testing.T) {
	body := `{"success":false,"message":"模型提供方错误","data":null,"code":"MPE-001","detailMessage":"{\"error\":{\"error_code\":\"COMPAT_001\",\"error_message\":\"Upstream returned error in compatible forwarding: upstream 500 Internal Server Error: {\\\"request_id\\\":\\\"2d363d9d-e2a0-4311-b5a7-3b76fff23a3a\\\",\\\"code\\\":\\\"ServiceUnavailable\\\",\\\"message\\\":\\\"<503> InternalError.Algo: An error occurred in model serving, error message is: [Too many requests. Your requests are being throttled due to system capacity limits. Please try again later.]\\\"}\",\"error_message_cn\":\"兼容转发上游返回错误: upstream 500 Internal Server Error: {\\\"request_id\\\":\\\"2d363d9d-e2a0-4311-b5a7-3b76fff23a3a\\\",\\\"code\\\":\\\"ServiceUnavailable\\\",\\\"message\\\":\\\"<503> InternalError.Algo: An error occurred in model serving, error message is: [Too many requests. Your requests are being throttled due to system capacity limits. Please try again later.]\\\"}\"}}"}`
	resp := responseWithBody(http.StatusBadRequest, body)

	retryable, errMsg := checkFor400Error(resp, []byte(body))
	if !retryable {
		t.Fatal("expected nested detailMessage throttle to be retryable")
	}
	if !strings.Contains(strings.ToLower(errMsg), "too many requests") {
		t.Fatalf("expected retryable message to include upstream throttle, got %q", errMsg)
	}
}

func TestCheckFor400ErrorLeavesValidationErrorNonRetryable(t *testing.T) {
	body := `{"error":{"message":"invalid model name","code":"invalid_request_error"}}`
	resp := responseWithBody(http.StatusBadRequest, body)

	retryable, errMsg := checkFor400Error(resp, []byte(body))
	if retryable {
		t.Fatalf("expected validation error to be non-retryable, got %q", errMsg)
	}
}

func TestDecodeBodyGzip(t *testing.T) {
	original := `{"hello":"world"}`
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(original)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	headers := http.Header{"Content-Encoding": []string{"gzip"}}
	got := decodeBody(headers, buf.Bytes())
	if string(got) != original {
		t.Fatalf("expected decoded body %q, got %q", original, got)
	}
}

func TestDecodeBodyZstd(t *testing.T) {
	original := `event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"service unavailable"}}`
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write([]byte(original)); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}

	headers := http.Header{"Content-Encoding": []string{"zstd"}}
	got := decodeBody(headers, buf.Bytes())
	if string(got) != original {
		t.Fatalf("expected decoded body %q, got %q", original, got)
	}
}

func TestDecodeBodyIdentityPassthrough(t *testing.T) {
	body := []byte(`{"plain":"json"}`)
	got := decodeBody(http.Header{}, body)
	if !bytes.Equal(got, body) {
		t.Fatalf("expected identity passthrough, got %q", got)
	}
}

func TestDecodeBodyUnknownEncodingFallsBack(t *testing.T) {
	body := []byte("not actually brotli")
	headers := http.Header{"Content-Encoding": []string{"br"}}
	got := decodeBody(headers, body)
	if !bytes.Equal(got, body) {
		t.Fatalf("expected raw body fallback for unsupported encoding, got %q", got)
	}
}

func TestTruncateBodyScrubsControlCharacters(t *testing.T) {
	// BEL, ESC, CR, NUL — all of which would corrupt or hijack the terminal
	// when written to the proxy's stdout via the logger.
	dirty := []byte("event: hi\x07\x1b]9;evil\x07\x00\x0dmore")
	got := truncateBody(dirty, 200)
	for _, ch := range []rune{'\x07', '\x1b', '\x00', '\x0d'} {
		if strings.ContainsRune(got, ch) {
			t.Fatalf("expected control character %#x to be scrubbed, got %q", ch, got)
		}
	}
	if !strings.Contains(got, "event: hi") {
		t.Fatalf("expected readable text to survive sanitization, got %q", got)
	}
}

func TestOSC9NotificationSequenceSanitizesControlCharacters(t *testing.T) {
	seq := osc9NotificationSequence("Encore retry\n1/3: too many requests\x1b]9;injected\x07")

	if !strings.HasPrefix(seq, "\x1b]9;") {
		t.Fatalf("expected OSC 9 prefix, got %q", seq)
	}
	if !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("expected BEL terminator, got %q", seq)
	}

	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]9;"), "\x07")
	if strings.ContainsAny(payload, "\x00\x07\x1b\n\r\t") {
		t.Fatalf("payload contains unsafe control characters: %q", payload)
	}
	if !strings.Contains(payload, "Encore retry 1/3: too many requests") {
		t.Fatalf("expected sanitized retry message, got %q", payload)
	}
}

func TestRetryableStatusReasonIncludesStatusAndBody(t *testing.T) {
	got := retryableStatusReason(http.StatusTooManyRequests, `{"error":"too many requests"}`)
	want := `HTTP 429 Too Many Requests: {"error":"too many requests"}`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func responseWithBody(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
