package websocket

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	testAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

func TestCandidateRecognizesHTTP11UpgradeIntent(t *testing.T) {
	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{name: "connection list", connection: "keep-alive, UpGrAdE", want: true},
		{name: "upgrade list", upgrade: "h2c, WebSocket", want: true},
		{name: "neither", connection: "keep-alive", upgrade: "h2c"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.test/events", nil)
			request.Header.Set("Connection", test.connection)
			request.Header.Set("Upgrade", test.upgrade)
			if got := Candidate(request); got != test.want {
				t.Fatalf("Candidate() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestValidateRequestCapturesCanonicalHandshake(t *testing.T) {
	request := validHandshakeRequest()
	request.Header.Set("Sec-WebSocket-Protocol", "chat, superchat")
	request.Header.Add("Sec-WebSocket-Extensions", "permessage-deflate; client_max_window_bits")
	request.Header.Add("Sec-WebSocket-Extensions", "x-example")

	got, err := ValidateRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want := RequestHandshake{
		Key:        testKey,
		Protocols:  []string{"chat", "superchat"},
		Extensions: []string{"permessage-deflate; client_max_window_bits", "x-example"},
	}
	if !got.Equal(want) || !want.Equal(got) {
		t.Fatalf("ValidateRequest() = %+v, want %+v", got, want)
	}
}

func TestValidateRequestRejectsMalformedHandshakeWithoutLeakingKey(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "protocol", mutate: func(request *http.Request) { request.ProtoMajor = 2; request.ProtoMinor = 0 }},
		{name: "method", mutate: func(request *http.Request) { request.Method = http.MethodPost }},
		{name: "connection", mutate: func(request *http.Request) { request.Header.Set("Connection", "keep-alive") }},
		{name: "upgrade", mutate: func(request *http.Request) { request.Header.Set("Upgrade", "h2c") }},
		{name: "version", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Version", "12") }},
		{name: "multiple versions", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Version", "13, 13") }},
		{name: "invalid base64 key", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Key", "not-base64") }},
		{name: "wrong key length", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Key", "c2hvcnQ=") }},
		{name: "multiple keys", mutate: func(request *http.Request) { request.Header.Add("Sec-WebSocket-Key", "AAAAAAAAAAAAAAAAAAAAAA==") }},
		{name: "body", mutate: func(request *http.Request) {
			body := strings.NewReader("payload")
			request.Body = http.NoBody
			request.ContentLength = int64(body.Len())
		}},
		{name: "invalid subprotocol", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Protocol", "chat, bad protocol") }},
		{name: "empty extension", mutate: func(request *http.Request) { request.Header.Set("Sec-WebSocket-Extensions", "x-test,") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validHandshakeRequest()
			test.mutate(request)
			_, err := ValidateRequest(request)
			if !errors.Is(err, ErrInvalidHandshake) {
				t.Fatalf("ValidateRequest() error = %v", err)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error leaked WebSocket key: %v", err)
			}
		})
	}
}

func TestCanonicalizeRequestHeadersRemovesHopHeadersAndRestoresHandshake(t *testing.T) {
	captured, err := ValidateRequest(validHandshakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{
		"Connection":             {"keep-alive, X-Remove"},
		"Upgrade":                {"changed"},
		"X-Remove":               {"secret"},
		"Keep-Alive":             {"timeout=5"},
		"Proxy-Connection":       {"keep-alive"},
		"Transfer-Encoding":      {"chunked"},
		"Sec-Websocket-Key":      {"changed"},
		"Sec-Websocket-Version":  {"12"},
		"Sec-Websocket-Protocol": {"changed"},
		"X-Application":          {"kept"},
	}
	captured.Protocols = []string{"chat", "superchat"}
	captured.Extensions = []string{"x-example"}

	CanonicalizeRequestHeaders(header, captured)
	if header.Get("Connection") != "Upgrade" || header.Get("Upgrade") != "websocket" ||
		header.Get("Sec-WebSocket-Key") != testKey || header.Get("Sec-WebSocket-Version") != "13" ||
		header.Get("Sec-WebSocket-Protocol") != "chat, superchat" ||
		header.Get("Sec-WebSocket-Extensions") != "x-example" || header.Get("X-Application") != "kept" {
		t.Fatalf("canonical headers = %#v", header)
	}
	for _, name := range []string{"X-Remove", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding"} {
		if _, exists := header[http.CanonicalHeaderKey(name)]; exists {
			t.Fatalf("hop header %q remains in %#v", name, header)
		}
	}
}

func TestValidateResponseAndFinalResponse(t *testing.T) {
	request := RequestHandshake{Key: testKey, Protocols: []string{"chat", "superchat"}, Extensions: []string{"x-example"}}
	response := validHandshakeResponse()
	response.Header.Set("Sec-WebSocket-Protocol", "superchat")
	response.Header.Set("Sec-WebSocket-Extensions", "x-example")

	negotiated, err := ValidateResponse(request, response)
	if err != nil {
		t.Fatal(err)
	}
	want := ResponseHandshake{Accept: testAccept, Protocol: "superchat", Extensions: []string{"x-example"}}
	if negotiated.Accept != want.Accept || negotiated.Protocol != want.Protocol ||
		len(negotiated.Extensions) != 1 || negotiated.Extensions[0] != want.Extensions[0] {
		t.Fatalf("ValidateResponse() = %+v, want %+v", negotiated, want)
	}
	if err := ValidateFinalResponse(negotiated, response); err != nil {
		t.Fatal(err)
	}
	response.Header.Set("Sec-WebSocket-Protocol", "chat")
	if err := ValidateFinalResponse(negotiated, response); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("ValidateFinalResponse() error = %v", err)
	}
}

func TestValidateResponseRejectsInvalidNegotiation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Response)
	}{
		{name: "status", mutate: func(response *http.Response) { response.StatusCode = http.StatusOK }},
		{name: "connection", mutate: func(response *http.Response) { response.Header.Set("Connection", "close") }},
		{name: "upgrade", mutate: func(response *http.Response) { response.Header.Set("Upgrade", "h2c") }},
		{name: "accept", mutate: func(response *http.Response) { response.Header.Set("Sec-WebSocket-Accept", "wrong") }},
		{name: "unoffered protocol", mutate: func(response *http.Response) { response.Header.Set("Sec-WebSocket-Protocol", "video") }},
		{name: "multiple protocols", mutate: func(response *http.Response) { response.Header.Set("Sec-WebSocket-Protocol", "chat, superchat") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validHandshakeResponse()
			test.mutate(response)
			_, err := ValidateResponse(RequestHandshake{Key: testKey, Protocols: []string{"chat", "superchat"}}, response)
			if !errors.Is(err, ErrInvalidHandshake) {
				t.Fatalf("ValidateResponse() error = %v", err)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error leaked WebSocket key: %v", err)
			}
		})
	}
}

func validHandshakeRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/events", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "WebSocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", testKey)
	return request
}

func validHandshakeResponse() *http.Response {
	response := &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: make(http.Header)}
	response.Header.Set("Connection", "Upgrade")
	response.Header.Set("Upgrade", "websocket")
	response.Header.Set("Sec-WebSocket-Accept", testAccept)
	return response
}
