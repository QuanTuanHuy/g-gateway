package plugin

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

const maxShortCircuitBodyBytes = 64 * 1024

// Action identifies how request-hook execution should proceed.
type Action uint8

const (
	// Continue tells the chain to invoke the next request hook.
	Continue Action = iota
	// ShortCircuit tells the chain to stop request hooks and return a validated
	// synthetic response.
	ShortCircuit
)

// ShortCircuitResponse describes a bounded synthetic HTTP response produced by
// a request hook. The chain clones Headers and Body before returning it.
type ShortCircuitResponse struct {
	// Status is an HTTP status code in the inclusive range 100 through 599.
	Status int
	// Headers contains response headers to clone into the synthetic response.
	Headers http.Header
	// Body contains at most 64 KiB of response data.
	Body []byte
	// Code is the bounded gateway error or outcome code recorded for the
	// response.
	Code string
}

// RequestResult is the outcome of one request hook. Err takes precedence over
// Action, and Response is required only for ShortCircuit.
type RequestResult struct {
	// Action tells the chain whether to continue or short-circuit.
	Action Action
	// Response supplies the synthetic response for ShortCircuit.
	Response *ShortCircuitResponse
	// Err reports hook failure and stops request-hook execution.
	Err error
}

// RequestHook mutates or rejects a request before upstream selection.
type RequestHook interface {
	// OnRequest executes one request-phase hook.
	OnRequest(*requestctx.Context, *http.Request) RequestResult
}

// ResponseHook mutates a final upstream or synthetic response.
type ResponseHook interface {
	// OnResponse executes one response-phase hook.
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

// Chain is an immutable ordered set of compiled request and response hooks. A
// nil Chain behaves as an empty chain.
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

// Names returns a copy of the compiled plugin names in request order.
func (c *Chain) Names() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.names...)
}

// ScratchSlots returns the total number of contiguous request-owned scratch
// entries required by the chain.
func (c *Chain) ScratchSlots() int {
	if c == nil {
		return 0
	}
	return c.scratchSlots
}

// RunRequest invokes request hooks in deterministic order until they finish,
// fail, or return ShortCircuit. It validates and clones a short-circuit
// response before returning it.
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

// RunResponse invokes all compiled response hooks in deterministic response
// order and stops at the first error.
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
