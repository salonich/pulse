package provider

import "testing"

func TestFromHost(t *testing.T) {
	cases := map[string]string{
		"api.anthropic.com":                 Anthropic,
		"API.ANTHROPIC.COM":                 Anthropic,
		"api.openai.com":                    OpenAI,
		"openai-proxy.acme.io":              OpenAI,
		"mockllm:8080":                      Anthropic, // mock speaks Anthropic dialect
		"some.random.host":                  Custom,
		"":                                  Custom,
		"generativelanguage.googleapis.com": Custom,
	}
	for host, want := range cases {
		if got := FromHost(host); got != want {
			t.Errorf("FromHost(%q): want %q, got %q", host, want, got)
		}
	}
}

func TestFromPath(t *testing.T) {
	cases := []struct {
		path         string
		wantProvider string
		wantTail     string
		wantOK       bool
	}{
		{"/anthropic/v1/messages", Anthropic, "/v1/messages", true},
		{"/openai/v1/chat/completions", OpenAI, "/v1/chat/completions", true},
		{"/anthropic/", Anthropic, "/", true},
		{"/healthz", "", "", false},
		{"/google/v1/models", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		p, tail, ok := FromPath(c.path)
		if p != c.wantProvider || tail != c.wantTail || ok != c.wantOK {
			t.Errorf("FromPath(%q): got (%q,%q,%v), want (%q,%q,%v)",
				c.path, p, tail, ok, c.wantProvider, c.wantTail, c.wantOK)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	cases := map[string]string{
		Anthropic: "https://api.anthropic.com",
		OpenAI:    "https://api.openai.com",
		Custom:    "",
		"":        "",
	}
	for p, want := range cases {
		if got := UpstreamURL(p); got != want {
			t.Errorf("UpstreamURL(%q): want %q, got %q", p, want, got)
		}
	}
}

func TestExtractUsage_Anthropic(t *testing.T) {
	body := []byte(`{
		"id": "msg_01",
		"model": "claude-sonnet-4-20250514",
		"usage": {"input_tokens": 123, "output_tokens": 45}
	}`)
	model, in, out := ExtractUsage(body)
	if model != "claude-sonnet-4-20250514" || in != 123 || out != 45 {
		t.Errorf("got model=%q in=%d out=%d", model, in, out)
	}
}

func TestExtractUsage_OpenAI(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-1",
		"model": "gpt-4o-mini",
		"usage": {"prompt_tokens": 11, "completion_tokens": 22, "total_tokens": 33}
	}`)
	model, in, out := ExtractUsage(body)
	if model != "gpt-4o-mini" || in != 11 || out != 22 {
		t.Errorf("got model=%q in=%d out=%d", model, in, out)
	}
}

func TestExtractUsage_RingBufferGarbage(t *testing.T) {
	body := []byte(`xx\x00\xff{"model":"claude-3-5","usage":{"input_tokens":1,"output_tokens":2}}trailing junk`)
	model, in, out := ExtractUsage(body)
	if model != "claude-3-5" || in != 1 || out != 2 {
		t.Errorf("got model=%q in=%d out=%d", model, in, out)
	}
}

func TestExtractUsage_Truncated(t *testing.T) {
	body := []byte(`{"model":"claude-3-haiku","content":[{"type":"text","text":"streaming...`)
	model, in, out := ExtractUsage(body)
	if model != "" || in != 0 || out != 0 {
		t.Errorf("truncated body should yield zeros, got model=%q in=%d out=%d", model, in, out)
	}
}

func TestExtractUsage_NotJSON(t *testing.T) {
	cases := map[string][]byte{
		"empty":    []byte(""),
		"no brace": []byte("HTTP/1.1 503 unavailable"),
		"only {":   []byte("{ no closing brace"),
		"only }":   []byte("} stray"),
		"empty obj": []byte("{}"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			model, in, out := ExtractUsage(b)
			if model != "" || in != 0 || out != 0 {
				t.Errorf("want zeros, got model=%q in=%d out=%d", model, in, out)
			}
		})
	}
}
