package plugin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

type requestIDConfig struct {
	HeaderName     string `json:"header_name"`
	MaxInputLength int    `json:"max_input_length"`
}

type requestIDPlugin struct {
	headerName string
	maxLength  int
	random     io.Reader
}

func requestIDDefinition(random io.Reader) Definition {
	return Definition{
		Name:          "request-id",
		Version:       "1.0.0",
		RequestOrder:  100,
		ResponseOrder: 900,
		Compile: func(raw json.RawMessage) (CompiledPlugin, error) {
			if random == nil {
				return CompiledPlugin{}, fmt.Errorf("secure random reader is nil")
			}
			config := requestIDConfig{
				HeaderName:     "X-Request-ID",
				MaxInputLength: 128,
			}
			if err := decodeStrictPluginJSON(raw, &config); err != nil {
				return CompiledPlugin{}, err
			}
			headerName, err := validateMutableHeaderName(config.HeaderName)
			if err != nil {
				return CompiledPlugin{}, fmt.Errorf("header_name: %w", err)
			}
			if config.MaxInputLength < 1 || config.MaxInputLength > 1024 {
				return CompiledPlugin{}, fmt.Errorf("max_input_length: must be between 1 and 1024")
			}
			plugin := &requestIDPlugin{
				headerName: headerName,
				maxLength:  config.MaxInputLength,
				random:     random,
			}
			return CompiledPlugin{Request: plugin, Response: plugin}, nil
		},
	}
}

func (p *requestIDPlugin) OnRequest(state *requestctx.Context, request *http.Request) RequestResult {
	values := request.Header.Values(p.headerName)
	requestID := ""
	if len(values) == 1 && validInboundRequestID(values[0], p.maxLength) {
		requestID = values[0]
	} else {
		generated, err := generateUUIDv4(p.random)
		if err != nil {
			return RequestResult{Err: fmt.Errorf("generate request ID: %w", err)}
		}
		requestID = generated
	}
	state.RequestID = requestID
	request.Header.Set(p.headerName, requestID)
	return RequestResult{Action: Continue}
}

func (p *requestIDPlugin) OnResponse(state *requestctx.Context, response *http.Response) error {
	if response.Header == nil {
		response.Header = make(http.Header)
	}
	response.Header.Set(p.headerName, state.RequestID)
	return nil
}

func validInboundRequestID(value string, maxLength int) bool {
	if len(value) == 0 || len(value) > maxLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		current := value[i]
		if current >= 'a' && current <= 'z' ||
			current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' ||
			current == '.' ||
			current == '_' ||
			current == ':' ||
			current == '-' {
			continue
		}
		return false
	}
	return true
}

func generateUUIDv4(random io.Reader) (string, error) {
	var source [16]byte
	if _, err := io.ReadFull(random, source[:]); err != nil {
		return "", err
	}
	source[6] = (source[6] & 0x0f) | 0x40
	source[8] = (source[8] & 0x3f) | 0x80

	var encoded [36]byte
	hex.Encode(encoded[0:8], source[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], source[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], source[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], source[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], source[10:16])
	return string(encoded[:]), nil
}

func decodeStrictPluginJSON(raw json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return fmt.Errorf("decode trailing config: %w", err)
		}
		return fmt.Errorf("decode config: multiple JSON values are not allowed")
	}
	return nil
}

func validateMutableHeaderName(raw string) (string, error) {
	name := textproto.CanonicalMIMEHeaderKey(raw)
	if name == "" || !validHeaderToken(raw) {
		return "", fmt.Errorf("invalid header name %q", raw)
	}
	if _, reserved := reservedMutableHeaders[strings.ToLower(name)]; reserved {
		return "", fmt.Errorf("header %q is reserved", name)
	}
	return name, nil
}

var reservedMutableHeaders = map[string]struct{}{
	"connection":          {},
	"content-length":      {},
	"host":                {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func validHeaderToken(value string) bool {
	if value == "" || strings.HasPrefix(value, ":") {
		return false
	}
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}
