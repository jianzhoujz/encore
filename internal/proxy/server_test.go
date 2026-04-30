package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCheckFor400ErrorDetectsNestedDetailMessageThrottle(t *testing.T) {
	body := `{"success":false,"message":"模型提供方错误","data":null,"code":"MPE-001","detailMessage":"{\"error\":{\"error_code\":\"COMPAT_001\",\"error_message\":\"Upstream returned error in compatible forwarding: upstream 500 Internal Server Error: {\\\"request_id\\\":\\\"2d363d9d-e2a0-4311-b5a7-3b76fff23a3a\\\",\\\"code\\\":\\\"ServiceUnavailable\\\",\\\"message\\\":\\\"<503> InternalError.Algo: An error occurred in model serving, error message is: [Too many requests. Your requests are being throttled due to system capacity limits. Please try again later.]\\\"}\",\"error_message_cn\":\"兼容转发上游返回错误: upstream 500 Internal Server Error: {\\\"request_id\\\":\\\"2d363d9d-e2a0-4311-b5a7-3b76fff23a3a\\\",\\\"code\\\":\\\"ServiceUnavailable\\\",\\\"message\\\":\\\"<503> InternalError.Algo: An error occurred in model serving, error message is: [Too many requests. Your requests are being throttled due to system capacity limits. Please try again later.]\\\"}\"}}"}`
	resp := responseWithBody(http.StatusBadRequest, body)

	retryable, errMsg := checkFor400Error(resp)
	if !retryable {
		t.Fatal("expected nested detailMessage throttle to be retryable")
	}
	if !strings.Contains(strings.ToLower(errMsg), "too many requests") {
		t.Fatalf("expected retryable message to include upstream throttle, got %q", errMsg)
	}

	restored, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read restored body: %v", err)
	}
	if string(restored) != body {
		t.Fatal("response body was not restored after inspection")
	}
}

func TestCheckFor400ErrorLeavesValidationErrorNonRetryable(t *testing.T) {
	body := `{"error":{"message":"invalid model name","code":"invalid_request_error"}}`
	resp := responseWithBody(http.StatusBadRequest, body)

	retryable, errMsg := checkFor400Error(resp)
	if retryable {
		t.Fatalf("expected validation error to be non-retryable, got %q", errMsg)
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
