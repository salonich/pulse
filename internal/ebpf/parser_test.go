package ebpf

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseHTTP_Request(t *testing.T) {
	raw := []byte(
		"POST /v1/messages HTTP/1.1\r\n" +
			"Host: api.anthropic.com\r\n" +
			"Content-Type: application/json\r\n" +
			"Authorization: Bearer sk-xxx\r\n" +
			"\r\n" +
			`{"model":"claude-sonnet-4-20250514"}`)

	got := parseHTTP(raw)
	if got.Kind != Request {
		t.Fatalf("Kind: want Request, got %v", got.Kind)
	}
	if got.Method != "POST" {
		t.Errorf("Method: want POST, got %q", got.Method)
	}
	if got.Path != "/v1/messages" {
		t.Errorf("Path: want /v1/messages, got %q", got.Path)
	}
	if got.Host != "api.anthropic.com" {
		t.Errorf("Host: want api.anthropic.com, got %q", got.Host)
	}
	if got.ContentType != "application/json" {
		t.Errorf("ContentType: want application/json, got %q", got.ContentType)
	}
	if !bytes.Contains(got.Body, []byte(`"model"`)) {
		t.Errorf("Body should contain model, got %q", got.Body)
	}
}

func TestParseHTTP_Response(t *testing.T) {
	raw := []byte(
		"HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/json\r\n" +
			"Server: anthropic\r\n" +
			"\r\n" +
			`{"id":"msg_1","usage":{"input_tokens":42,"output_tokens":7}}`)

	got := parseHTTP(raw)
	if got.Kind != Response {
		t.Fatalf("Kind: want Response, got %v", got.Kind)
	}
	if got.StatusCode != 200 {
		t.Errorf("StatusCode: want 200, got %d", got.StatusCode)
	}
	if got.ContentType != "application/json" {
		t.Errorf("ContentType: want application/json, got %q", got.ContentType)
	}
	if !bytes.Contains(got.Body, []byte("input_tokens")) {
		t.Errorf("Body should contain usage, got %q", got.Body)
	}
}

func TestParseHTTP_NonHTTP(t *testing.T) {
	cases := map[string][]byte{
		"too short":     []byte("hi"),
		"random bytes":  []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09"),
		"tls handshake": []byte("\x16\x03\x01\x02\x00\x01\x00\x01\xfc"),
		"garbage line":  []byte("this is not http at all\r\nfoo: bar\r\n\r\n"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseHTTP(data)
			if got.Kind != NotHTTP {
				t.Errorf("want NotHTTP, got %v", got.Kind)
			}
		})
	}
}

func TestParseHTTP_HeaderTruncated(t *testing.T) {
	raw := []byte("GET /openai/v1/chat HTTP/1.1\r\nHost: api.openai.com\r\nUser-Agent: test")
	got := parseHTTP(raw)
	if got.Kind != Request {
		t.Fatalf("Kind: want Request, got %v", got.Kind)
	}
	if got.Host != "api.openai.com" {
		t.Errorf("Host: want api.openai.com, got %q", got.Host)
	}
	if len(got.Body) != 0 {
		t.Errorf("Body should be empty when no header terminator, got %q", got.Body)
	}
}

func TestParseHTTP_StatusCodes(t *testing.T) {
	cases := map[string]int{
		"HTTP/1.1 200 OK\r\n\r\n":                  200,
		"HTTP/1.1 429 Too Many Requests\r\n\r\n":   429,
		"HTTP/2 500 Internal Server Error\r\n\r\n": 500,
		"HTTP/1.0 404 Not Found\r\n\r\n":           404,
	}
	for raw, want := range cases {
		got := parseHTTP([]byte(raw))
		if got.Kind != Response {
			t.Errorf("%q: want Response, got %v", raw, got.Kind)
		}
		if got.StatusCode != want {
			t.Errorf("%q: status want %d, got %d", raw, want, got.StatusCode)
		}
	}
}

func TestParseHTTP_HeadersAreCaseInsensitive(t *testing.T) {
	raw := []byte(
		"POST /v1/messages HTTP/1.1\r\n" +
			"HOST: api.anthropic.com\r\n" +
			"content-TYPE: application/json\r\n" +
			"\r\n")

	got := parseHTTP(raw)
	if got.Host != "api.anthropic.com" {
		t.Errorf("Host: want api.anthropic.com, got %q", got.Host)
	}
	if got.ContentType != "application/json" {
		t.Errorf("ContentType: want application/json, got %q", got.ContentType)
	}
}

func TestParseHTTP_HostWithPort(t *testing.T) {
	raw := []byte("POST /v1/messages HTTP/1.1\r\nHost: localhost:8080\r\n\r\n")
	got := parseHTTP(raw)
	if !strings.HasPrefix(got.Host, "localhost") {
		t.Errorf("Host: want localhost prefix, got %q", got.Host)
	}
}
