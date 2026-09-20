package accesslog

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"
)

type eventWire struct {
	Schema            string `json:"schema"`
	Timestamp         string `json:"timestamp"`
	RequestID         string `json:"request_id"`
	Revision          uint64 `json:"revision"`
	Method            string `json:"method"`
	Protocol          string `json:"protocol"`
	RouteID           string `json:"route_id"`
	ServiceID         string `json:"service_id"`
	UpstreamID        string `json:"upstream_id"`
	EndpointID        string `json:"endpoint_id"`
	ResponseSource    string `json:"response_source"`
	Status            int    `json:"status"`
	RequestBodyBytes  uint64 `json:"request_body_bytes"`
	ResponseBodyBytes uint64 `json:"response_body_bytes"`
	Attempts          int    `json:"attempts"`
	UpstreamOutcome   string `json:"upstream_outcome"`
	RetrySuppressed   string `json:"retry_suppressed"`
	ErrorCode         string `json:"error_code"`
	Termination       string `json:"termination"`
	DurationUS        uint64 `json:"duration_us"`
	UpstreamWaitUS    uint64 `json:"upstream_wait_us"`
	ResponseStreamUS  uint64 `json:"response_stream_us"`
}

func encode(event Event) ([]byte, error) {
	var encoder eventEncoder
	encoded, err := encoder.encodeWire(toWire(event))
	return append([]byte(nil), encoded...), err
}

func encodeWire(wire eventWire) ([]byte, error) {
	var encoder eventEncoder
	encoded, err := encoder.encodeWire(wire)
	return append([]byte(nil), encoded...), err
}

func encodedEventSize(event Event) int {
	const emptyEvent = "{\"schema\":\"\",\"timestamp\":\"\",\"request_id\":\"\",\"revision\":0,\"method\":\"\",\"protocol\":\"\",\"route_id\":\"\",\"service_id\":\"\",\"upstream_id\":\"\",\"endpoint_id\":\"\",\"response_source\":\"\",\"status\":0,\"request_body_bytes\":0,\"response_body_bytes\":0,\"attempts\":0,\"upstream_outcome\":\"\",\"retry_suppressed\":\"\",\"error_code\":\"\",\"termination\":\"\",\"duration_us\":0,\"upstream_wait_us\":0,\"response_stream_us\":0}\n"
	size := len(emptyEvent)
	for _, value := range []string{
		event.Schema,
		event.Timestamp,
		event.RequestID,
		event.Method,
		event.Protocol,
		event.RouteID,
		event.ServiceID,
		event.UpstreamID,
		event.EndpointID,
		event.ResponseSource,
		event.UpstreamOutcome,
		event.RetrySuppressed,
		event.ErrorCode,
		event.Termination,
	} {
		size += jsonStringSize(value) - 2
	}
	size += uintDecimalSize(event.Revision) - 1
	size += intDecimalSize(event.Status) - 1
	size += uintDecimalSize(event.RequestBodyBytes) - 1
	size += uintDecimalSize(event.ResponseBodyBytes) - 1
	size += intDecimalSize(event.Attempts) - 1
	size += uintDecimalSize(event.DurationUS) - 1
	size += uintDecimalSize(event.UpstreamWaitUS) - 1
	size += uintDecimalSize(event.ResponseStreamUS) - 1
	return size
}

type eventEncoder struct {
	buffer bytes.Buffer
}

func (e *eventEncoder) encodeWire(wire eventWire) ([]byte, error) {
	e.buffer.Reset()
	if err := json.NewEncoder(&e.buffer).Encode(wire); err != nil {
		return nil, err
	}
	if e.buffer.Len() > MaxEncodedBytes {
		return nil, errors.New("access event exceeds encoded size limit")
	}
	return e.buffer.Bytes(), nil
}

func jsonStringSize(value string) int {
	size := 2
	for _, current := range value {
		switch current {
		case '\\', '"', '\b', '\f', '\n', '\r', '\t':
			size += 2
		case '<', '>', '&', '\u2028', '\u2029':
			size += 6
		default:
			if current < 0x20 {
				size += 6
			} else {
				size += utf8.RuneLen(current)
			}
		}
	}
	return size
}

func uintDecimalSize(value uint64) int {
	return len(strconv.FormatUint(value, 10))
}

func intDecimalSize(value int) int {
	return len(strconv.Itoa(value))
}

func toWire(event Event) eventWire {
	return eventWire{
		Schema:            event.Schema,
		Timestamp:         event.Timestamp,
		RequestID:         event.RequestID,
		Revision:          event.Revision,
		Method:            event.Method,
		Protocol:          event.Protocol,
		RouteID:           event.RouteID,
		ServiceID:         event.ServiceID,
		UpstreamID:        event.UpstreamID,
		EndpointID:        event.EndpointID,
		ResponseSource:    event.ResponseSource,
		Status:            event.Status,
		RequestBodyBytes:  event.RequestBodyBytes,
		ResponseBodyBytes: event.ResponseBodyBytes,
		Attempts:          event.Attempts,
		UpstreamOutcome:   event.UpstreamOutcome,
		RetrySuppressed:   event.RetrySuppressed,
		ErrorCode:         event.ErrorCode,
		Termination:       event.Termination,
		DurationUS:        event.DurationUS,
		UpstreamWaitUS:    event.UpstreamWaitUS,
		ResponseStreamUS:  event.ResponseStreamUS,
	}
}
