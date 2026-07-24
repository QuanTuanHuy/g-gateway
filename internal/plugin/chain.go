package plugin

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

const maxShortCircuitBodyBytes = 64 * 1024

type Action uint8

const (
	Continue Action = iota
	ShortCircuit
)

type ShortCircuitResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
	Code    string
}

type RequestResult struct {
	Action   Action
	Response *ShortCircuitResponse
	Err      error
}

type RequestHook interface {
	OnRequest(*requestctx.Context, *http.Request) RequestResult
}

type ResponseHook interface {
	OnResponse(*requestctx.Context, *http.Response) error
}

type compiledEntry struct {
	name         string
	definition   Definition
	plugin       CompiledPlugin
	scratchStart int
	scratchEnd   int
}

type requestEntry struct {
	name  string
	order int
	hook  RequestHook
}

type responseEntry struct {
	name  string
	order int
	hook  ResponseHook
}

type Chain struct {
	names        []string
	request      []requestEntry
	response     []responseEntry
	scratchSlots int
}

func buildChain(entries []compiledEntry) *Chain {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].definition.RequestOrder != entries[j].definition.RequestOrder {
			return entries[i].definition.RequestOrder < entries[j].definition.RequestOrder
		}
		return entries[i].name < entries[j].name
	})
	chain := &Chain{
		names: make([]string, 0, len(entries)),
	}
	for i := range entries {
		entries[i].scratchStart = chain.scratchSlots
		chain.scratchSlots += entries[i].plugin.ScratchSlots
		entries[i].scratchEnd = chain.scratchSlots
		chain.names = append(chain.names, entries[i].name)
		if entries[i].plugin.Request != nil {
			chain.request = append(chain.request, requestEntry{
				name:  entries[i].name,
				order: entries[i].definition.RequestOrder,
				hook:  entries[i].plugin.Request,
			})
		}
		if entries[i].plugin.Response != nil {
			chain.response = append(chain.response, responseEntry{
				name:  entries[i].name,
				order: entries[i].definition.ResponseOrder,
				hook:  entries[i].plugin.Response,
			})
		}
	}
	sort.Slice(chain.response, func(i, j int) bool {
		if chain.response[i].order != chain.response[j].order {
			return chain.response[i].order < chain.response[j].order
		}
		return chain.response[i].name < chain.response[j].name
	})
	return chain
}

func (c *Chain) Names() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.names...)
}

func (c *Chain) ScratchSlots() int {
	if c == nil {
		return 0
	}
	return c.scratchSlots
}

func (c *Chain) RunRequest(state *requestctx.Context, request *http.Request) RequestResult {
	if c == nil {
		return RequestResult{Action: Continue}
	}
	for _, entry := range c.request {
		result := entry.hook.OnRequest(state, request)
		if result.Err != nil {
			return RequestResult{
				Action: Continue,
				Err:    fmt.Errorf("plugin %q request phase: %w", entry.name, result.Err),
			}
		}
		switch result.Action {
		case Continue:
			continue
		case ShortCircuit:
			response, err := cloneAndValidateShortCircuit(result.Response)
			if err != nil {
				return RequestResult{
					Action: Continue,
					Err:    fmt.Errorf("plugin %q request phase: %w", entry.name, err),
				}
			}
			return RequestResult{Action: ShortCircuit, Response: response}
		default:
			return RequestResult{
				Action: Continue,
				Err:    fmt.Errorf("plugin %q request phase: invalid action %d", entry.name, result.Action),
			}
		}
	}
	return RequestResult{Action: Continue}
}

func (c *Chain) RunResponse(state *requestctx.Context, response *http.Response) error {
	if c == nil {
		return nil
	}
	for _, entry := range c.response {
		if err := entry.hook.OnResponse(state, response); err != nil {
			return fmt.Errorf("plugin %q response phase: %w", entry.name, err)
		}
	}
	return nil
}

func cloneAndValidateShortCircuit(response *ShortCircuitResponse) (*ShortCircuitResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("short-circuit response is required")
	}
	if response.Status < 100 || response.Status > 599 {
		return nil, fmt.Errorf("short-circuit status %d is invalid", response.Status)
	}
	if len(response.Body) > maxShortCircuitBodyBytes {
		return nil, fmt.Errorf(
			"short-circuit body is %d bytes, limit is %d",
			len(response.Body),
			maxShortCircuitBodyBytes,
		)
	}
	return &ShortCircuitResponse{
		Status:  response.Status,
		Headers: response.Headers.Clone(),
		Body:    append([]byte(nil), response.Body...),
		Code:    response.Code,
	}, nil
}
