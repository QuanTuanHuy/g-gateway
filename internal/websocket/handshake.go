package websocket

import (
	"crypto/sha1" // #nosec G505 -- RFC 6455 requires SHA-1 for Sec-WebSocket-Accept.
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrInvalidHandshake identifies an invalid classic WebSocket handshake.
var ErrInvalidHandshake = errors.New("invalid WebSocket handshake")

// RequestHandshake contains validated request-side handshake semantics.
type RequestHandshake struct {
	// Key is the single RFC 6455 nonce supplied by the downstream client.
	Key string
	// Protocols contains offered subprotocols in wire order.
	Protocols []string
	// Extensions contains offered extension values in wire order.
	Extensions []string
}

// ResponseHandshake contains validated upstream negotiation semantics.
type ResponseHandshake struct {
	// Accept is the validated response derived from the downstream key.
	Accept string
	// Protocol is the single selected subprotocol, or empty when none was selected.
	Protocol string
	// Extensions contains negotiated extension values in wire order.
	Extensions []string
}

// Candidate reports whether request expresses classic HTTP Upgrade intent.
func Candidate(request *http.Request) bool {
	if request == nil {
		return false
	}
	return hasToken(request.Header.Values("Connection"), "upgrade") ||
		hasToken(request.Header.Values("Upgrade"), "websocket")
}

// ValidateRequest validates and captures a classic RFC 6455 client handshake.
func ValidateRequest(request *http.Request) (RequestHandshake, error) {
	if request == nil {
		return RequestHandshake{}, invalid("request")
	}
	if request.ProtoMajor != 1 || request.ProtoMinor != 1 {
		return RequestHandshake{}, invalid("request protocol")
	}
	if request.Method != http.MethodGet {
		return RequestHandshake{}, invalid("request method")
	}
	if !hasToken(request.Header.Values("Connection"), "upgrade") {
		return RequestHandshake{}, invalid("request connection")
	}
	if !hasToken(request.Header.Values("Upgrade"), "websocket") {
		return RequestHandshake{}, invalid("request upgrade")
	}
	versions, ok := commaValues(request.Header.Values("Sec-WebSocket-Version"), true)
	if !ok || len(versions) != 1 || versions[0] != "13" {
		return RequestHandshake{}, invalid("request version")
	}
	keys := request.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 || strings.TrimSpace(keys[0]) != keys[0] || strings.Contains(keys[0], ",") {
		return RequestHandshake{}, invalid("request key")
	}
	decoded, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil || len(decoded) != 16 {
		return RequestHandshake{}, invalid("request key")
	}
	if request.ContentLength > 0 || (request.Body != nil && request.Body != http.NoBody) {
		return RequestHandshake{}, invalid("request body")
	}
	protocols, ok := commaValues(request.Header.Values("Sec-WebSocket-Protocol"), true)
	if !ok || !unique(protocols) {
		return RequestHandshake{}, invalid("request subprotocol")
	}
	for _, protocol := range protocols {
		if !validToken(protocol) {
			return RequestHandshake{}, invalid("request subprotocol")
		}
	}
	extensions, ok := commaValues(request.Header.Values("Sec-WebSocket-Extensions"), false)
	if !ok {
		return RequestHandshake{}, invalid("request extensions")
	}
	return RequestHandshake{Key: keys[0], Protocols: protocols, Extensions: extensions}, nil
}

// Equal reports whether two captured request handshakes have identical
// semantics.
func (h RequestHandshake) Equal(other RequestHandshake) bool {
	return h.Key == other.Key && slices.Equal(h.Protocols, other.Protocols) && slices.Equal(h.Extensions, other.Extensions)
}

// CanonicalizeRequestHeaders removes hop-by-hop fields and restores only the
// validated WebSocket handshake controls.
func CanonicalizeRequestHeaders(header http.Header, captured RequestHandshake) {
	connectionValues := append([]string(nil), header.Values("Connection")...)
	for _, value := range connectionValues {
		for _, token := range strings.Split(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
		"Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol",
		"Sec-WebSocket-Extensions",
	} {
		header.Del(name)
	}
	header.Set("Connection", "Upgrade")
	header.Set("Upgrade", "websocket")
	header.Set("Sec-WebSocket-Key", captured.Key)
	header.Set("Sec-WebSocket-Version", "13")
	if len(captured.Protocols) > 0 {
		header.Set("Sec-WebSocket-Protocol", strings.Join(captured.Protocols, ", "))
	}
	if len(captured.Extensions) > 0 {
		header.Set("Sec-WebSocket-Extensions", strings.Join(captured.Extensions, ", "))
	}
}

// ValidateResponse validates an upstream switching-protocols response against
// the downstream offer and captures its negotiation semantics.
func ValidateResponse(request RequestHandshake, response *http.Response) (ResponseHandshake, error) {
	if response == nil || response.StatusCode != http.StatusSwitchingProtocols {
		return ResponseHandshake{}, invalid("response status")
	}
	if !hasToken(response.Header.Values("Connection"), "upgrade") {
		return ResponseHandshake{}, invalid("response connection")
	}
	if !hasToken(response.Header.Values("Upgrade"), "websocket") {
		return ResponseHandshake{}, invalid("response upgrade")
	}
	acceptValues := response.Header.Values("Sec-WebSocket-Accept")
	expectedAccept := accept(request.Key)
	if len(acceptValues) != 1 || strings.TrimSpace(acceptValues[0]) != acceptValues[0] || acceptValues[0] != expectedAccept {
		return ResponseHandshake{}, invalid("response accept")
	}
	protocols, ok := commaValues(response.Header.Values("Sec-WebSocket-Protocol"), true)
	if !ok || len(protocols) > 1 {
		return ResponseHandshake{}, invalid("response subprotocol")
	}
	protocol := ""
	if len(protocols) == 1 {
		protocol = protocols[0]
		if !validToken(protocol) || !slices.Contains(request.Protocols, protocol) {
			return ResponseHandshake{}, invalid("response subprotocol")
		}
	}
	extensions, ok := commaValues(response.Header.Values("Sec-WebSocket-Extensions"), false)
	if !ok {
		return ResponseHandshake{}, invalid("response extensions")
	}
	return ResponseHandshake{Accept: expectedAccept, Protocol: protocol, Extensions: extensions}, nil
}

// ValidateFinalResponse verifies that response still represents the captured
// upstream negotiation after response plugins have run.
func ValidateFinalResponse(expected ResponseHandshake, response *http.Response) error {
	if response == nil || response.StatusCode != http.StatusSwitchingProtocols {
		return invalid("response status")
	}
	if !hasToken(response.Header.Values("Connection"), "upgrade") {
		return invalid("response connection")
	}
	if !hasToken(response.Header.Values("Upgrade"), "websocket") {
		return invalid("response upgrade")
	}
	acceptValues := response.Header.Values("Sec-WebSocket-Accept")
	if len(acceptValues) != 1 || strings.TrimSpace(acceptValues[0]) != expected.Accept {
		return invalid("response accept")
	}
	protocols, ok := commaValues(response.Header.Values("Sec-WebSocket-Protocol"), true)
	if !ok || len(protocols) > 1 {
		return invalid("response subprotocol")
	}
	protocol := ""
	if len(protocols) == 1 {
		protocol = protocols[0]
	}
	if protocol != expected.Protocol {
		return invalid("response subprotocol")
	}
	extensions, ok := commaValues(response.Header.Values("Sec-WebSocket-Extensions"), false)
	if !ok || !slices.Equal(extensions, expected.Extensions) {
		return invalid("response extensions")
	}
	return nil
}

func accept(key string) string {
	digest := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- required by RFC 6455.
	return base64.StdEncoding.EncodeToString(digest[:])
}

func invalid(category string) error {
	return fmt.Errorf("%w: %s", ErrInvalidHandshake, category)
}

func hasToken(values []string, want string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}

func commaValues(values []string, tokensOnly bool) ([]string, bool) {
	if len(values) == 0 {
		return nil, true
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || (tokensOnly && !validToken(part)) {
				return nil, false
			}
			out = append(out, part)
		}
	}
	return out, true
}

func unique(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character > 127 || character <= 31 || character == 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", character) {
			return false
		}
	}
	return true
}
