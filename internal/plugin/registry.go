package plugin

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

// CompiledPlugin contains immutable hooks and request scratch requirements
// produced by a Definition.
type CompiledPlugin struct {
	// Request is the optional request-phase hook.
	Request RequestHook
	// Response is the optional response-phase hook.
	Response ResponseHook
	// ScratchSlots is the non-negative number of request-owned scratch entries
	// reserved for this plugin.
	ScratchSlots int
}

// Definition describes one versioned plugin compiler and its deterministic
// request and response ordering.
type Definition struct {
	// Name uniquely identifies the plugin within a registry.
	Name string
	// Version labels the implementation version; the registry requires it but
	// does not interpret it.
	Version string
	// RequestOrder orders request hooks in ascending order.
	RequestOrder int
	// ResponseOrder orders response hooks in ascending order.
	ResponseOrder int
	// Compile validates raw JSON and returns an immutable compiled plugin.
	Compile func(json.RawMessage) (CompiledPlugin, error)
}

// Registry contains an immutable set of validated plugin definitions.
type Registry struct {
	definitions map[string]Definition
}

// NewBuiltinRegistry returns a registry containing the request-ID and
// header-rewrite plugins.
func NewBuiltinRegistry() (*Registry, error) {
	return NewRegistry(
		requestIDDefinition(rand.Reader),
		headerRewriteDefinition(),
	)
}

// NewRegistry validates definitions and returns an immutable registry. It
// rejects missing metadata or compilers, duplicate names, and colliding request
// or response order values.
func NewRegistry(definitions ...Definition) (*Registry, error) {
	registry := &Registry{definitions: make(map[string]Definition, len(definitions))}
	requestOrders := make(map[int]string, len(definitions))
	responseOrders := make(map[int]string, len(definitions))
	for i, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return nil, fmt.Errorf("plugin definition %d: name must not be empty", i)
		}
		if strings.TrimSpace(definition.Version) == "" {
			return nil, fmt.Errorf("plugin %q: version must not be empty", definition.Name)
		}
		if definition.Compile == nil {
			return nil, fmt.Errorf("plugin %q: compiler must not be nil", definition.Name)
		}
		if _, duplicate := registry.definitions[definition.Name]; duplicate {
			return nil, fmt.Errorf("duplicate plugin definition %q", definition.Name)
		}
		if previous, collision := requestOrders[definition.RequestOrder]; collision {
			return nil, fmt.Errorf(
				"plugin request order %d collides between %q and %q",
				definition.RequestOrder,
				previous,
				definition.Name,
			)
		}
		if previous, collision := responseOrders[definition.ResponseOrder]; collision {
			return nil, fmt.Errorf(
				"plugin response order %d collides between %q and %q",
				definition.ResponseOrder,
				previous,
				definition.Name,
			)
		}
		requestOrders[definition.RequestOrder] = definition.Name
		responseOrders[definition.ResponseOrder] = definition.Name
		registry.definitions[definition.Name] = definition
	}
	return registry, nil
}

// CompileChain validates and compiles service and route attachments into an
// immutable Chain. Enabled route attachments replace same-named service
// attachments, while disabled route attachments remove them. Input
// configuration bytes are cloned before compilation.
func (r *Registry) CompileChain(service, route []model.PluginAttachment) (*Chain, error) {
	if r == nil {
		return nil, fmt.Errorf("plugin registry is nil")
	}
	if err := validateAttachmentScope("service", service); err != nil {
		return nil, err
	}
	if err := validateAttachmentScope("route", route); err != nil {
		return nil, err
	}

	effective := make(map[string]model.PluginAttachment, len(service)+len(route))
	for _, attachment := range service {
		if attachment.Enabled {
			effective[attachment.Name] = cloneAttachment(attachment)
		}
	}
	for _, attachment := range route {
		if !attachment.Enabled {
			delete(effective, attachment.Name)
			continue
		}
		effective[attachment.Name] = cloneAttachment(attachment)
	}

	names := make([]string, 0, len(effective))
	for name := range effective {
		names = append(names, name)
	}
	sortStrings(names)

	entries := make([]compiledEntry, 0, len(names))
	for _, name := range names {
		definition, ok := r.definitions[name]
		if !ok {
			return nil, fmt.Errorf("unknown plugin %q", name)
		}
		attachment := effective[name]
		compiled, err := definition.Compile(append(json.RawMessage(nil), attachment.RawConfig...))
		if err != nil {
			return nil, fmt.Errorf("compile plugin %q: %w", name, err)
		}
		if compiled.ScratchSlots < 0 {
			return nil, fmt.Errorf("compile plugin %q: scratch slots must not be negative", name)
		}
		entries = append(entries, compiledEntry{
			name:       name,
			definition: definition,
			plugin:     compiled,
		})
	}
	return buildChain(entries), nil
}

func validateAttachmentScope(scope string, attachments []model.PluginAttachment) error {
	seen := make(map[string]struct{}, len(attachments))
	for i, attachment := range attachments {
		if strings.TrimSpace(attachment.Name) == "" {
			return fmt.Errorf("%s plugins[%d]: name must not be empty", scope, i)
		}
		if _, duplicate := seen[attachment.Name]; duplicate {
			return fmt.Errorf("%s plugins: duplicate plugin name %q", scope, attachment.Name)
		}
		seen[attachment.Name] = struct{}{}
	}
	return nil
}

func cloneAttachment(attachment model.PluginAttachment) model.PluginAttachment {
	attachment.RawConfig = append(json.RawMessage(nil), attachment.RawConfig...)
	return attachment
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
