package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzRequestHandshake(f *testing.F) {
	f.Add("Upgrade", "websocket", "13", testKey, "chat", "permessage-deflate")
	f.Add("close", "h2c", "12", "secret-invalid-key", "bad protocol", "x-test,")
	f.Fuzz(func(t *testing.T, connection, upgrade, version, key, protocols, extensions string) {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/events", nil)
		request.Header.Set("Connection", connection)
		request.Header.Set("Upgrade", upgrade)
		request.Header.Set("Sec-WebSocket-Version", version)
		request.Header.Set("Sec-WebSocket-Key", key)
		request.Header.Set("Sec-WebSocket-Protocol", protocols)
		request.Header.Set("Sec-WebSocket-Extensions", extensions)

		first, firstErr := ValidateRequest(request)
		second, secondErr := ValidateRequest(request)
		if (firstErr == nil) != (secondErr == nil) || (firstErr == nil && !first.Equal(second)) {
			t.Fatalf("classification was nondeterministic")
		}
		if firstErr != nil && strings.HasPrefix(key, "secret-") && strings.Contains(firstErr.Error(), key) {
			t.Fatalf("validation error leaked key")
		}
	})
}

func FuzzResponseHandshake(f *testing.F) {
	f.Add(http.StatusSwitchingProtocols, "Upgrade", "websocket", testAccept, "chat", "x-example")
	f.Add(http.StatusOK, "close", "h2c", "secret-invalid-accept", "video", "x-test,")
	f.Fuzz(func(t *testing.T, status int, connection, upgrade, acceptValue, protocol, extensions string) {
		request := RequestHandshake{Key: testKey, Protocols: []string{"chat"}, Extensions: []string{"x-example"}}
		response := &http.Response{StatusCode: status, Header: make(http.Header)}
		response.Header.Set("Connection", connection)
		response.Header.Set("Upgrade", upgrade)
		response.Header.Set("Sec-WebSocket-Accept", acceptValue)
		response.Header.Set("Sec-WebSocket-Protocol", protocol)
		response.Header.Set("Sec-WebSocket-Extensions", extensions)

		first, firstErr := ValidateResponse(request, response)
		second, secondErr := ValidateResponse(request, response)
		if (firstErr == nil) != (secondErr == nil) || (firstErr == nil &&
			(first.Accept != second.Accept || first.Protocol != second.Protocol || !slicesEqual(first.Extensions, second.Extensions))) {
			t.Fatalf("classification was nondeterministic")
		}
		if firstErr != nil && (strings.Contains(firstErr.Error(), testKey) ||
			(strings.HasPrefix(acceptValue, "secret-") && strings.Contains(firstErr.Error(), acceptValue))) {
			t.Fatalf("validation error leaked handshake secret")
		}
	})
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
