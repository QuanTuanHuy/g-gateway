package plugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type CompiledPlugin struct {
	Request      RequestHook
	Response     ResponseHook
	ScratchSlots int
}

type Definition struct {
	Name          string
	Version       string
	RequestOrder  int
	ResponseOrder int
	Compile       func(json.RawMessage) (CompiledPlugin, error)
}

type Registry struct {
	definitions map[string]Definition
}

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
