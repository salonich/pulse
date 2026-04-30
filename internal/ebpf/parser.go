package ebpf

import (
	"bytes"
	"strconv"
	"strings"
)

// HTTPKind is what we recognised in a captured chunk.
type HTTPKind int

const (
	NotHTTP HTTPKind = iota
	Request
	Response
)

// ParsedHTTP holds just the fields we care about for trace capture.
type ParsedHTTP struct {
	Kind        HTTPKind
	Method      string // request
	Path        string // request
	Host        string // request
	StatusCode  int    // response
	ContentType string
	Body        []byte // portion of body we captured
}

// parseHTTP inspects a chunk of bytes and classifies it as request / response.
// We only look at the first chunk of a write/read — that's where the start-line
// lives. Body may be truncated; that's fine for extracting JSON usage fields
// which appear near the end of short responses.
func parseHTTP(data []byte) ParsedHTTP {
	if len(data) < 8 {
		return ParsedHTTP{Kind: NotHTTP}
	}

	headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
	var header, body []byte
	if headerEnd >= 0 {
		header = data[:headerEnd]
		body = data[headerEnd+4:]
	} else {
		header = data
	}

	firstLineEnd := bytes.Index(header, []byte("\r\n"))
	if firstLineEnd < 0 {
		firstLineEnd = len(header)
	}
	firstLine := string(header[:firstLineEnd])

	p := ParsedHTTP{Body: body}

	if strings.HasPrefix(firstLine, "HTTP/") {
		p.Kind = Response
		parts := strings.SplitN(firstLine, " ", 3)
		if len(parts) >= 2 {
			if code, err := strconv.Atoi(parts[1]); err == nil {
				p.StatusCode = code
			}
		}
	} else {
		// Check if it looks like a request line: METHOD /path HTTP/x.y
		parts := strings.SplitN(firstLine, " ", 3)
		if len(parts) == 3 && strings.HasPrefix(parts[2], "HTTP/") {
			p.Kind = Request
			p.Method = parts[0]
			p.Path = parts[1]
		} else {
			return ParsedHTTP{Kind: NotHTTP}
		}
	}

	rest := header[firstLineEnd:]
	for _, line := range bytes.Split(rest, []byte("\r\n")) {
		if len(line) == 0 {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(line[:colon])))
		val := strings.TrimSpace(string(line[colon+1:]))
		switch name {
		case "host":
			p.Host = val
		case "content-type":
			p.ContentType = val
		}
	}
	return p
}
