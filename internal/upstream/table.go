package upstream

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

var newRuntime = New

type Table struct {
	byID      map[string]*Runtime
	resources []model.Upstream
}

func NewTable(resources []model.Upstream) (*Table, error) {
	canonical, err := canonicalUpstreams(resources)
	if err != nil {
		return nil, err
	}

	table := &Table{
		byID:      make(map[string]*Runtime, len(canonical)),
		resources: canonical,
	}
	for _, resource := range canonical {
		runtime, err := newRuntime(resource)
		if err != nil {
			table.CloseIdleConnections()
			return nil, fmt.Errorf("create upstream runtime %q: %w", resource.ID, err)
		}
		table.byID[resource.ID] = runtime
	}
	return table, nil
}

func (t *Table) Get(id string) (*Runtime, bool) {
	runtime, ok := t.byID[id]
	return runtime, ok
}

func (t *Table) ValidateResources(resources []model.Upstream) error {
	canonical, err := canonicalUpstreams(resources)
	if err != nil {
		return fmt.Errorf("UPSTREAM_SET_IMMUTABLE: %w", err)
	}
	if !reflect.DeepEqual(t.resources, canonical) {
		return fmt.Errorf("UPSTREAM_SET_IMMUTABLE: upstream resources differ from the bootstrap set")
	}
	return nil
}

func (t *Table) CloseIdleConnections() {
	if t == nil {
		return
	}
	for _, resource := range t.resources {
		if runtime := t.byID[resource.ID]; runtime != nil {
			runtime.CloseIdleConnections()
		}
	}
}

func canonicalUpstreams(resources []model.Upstream) ([]model.Upstream, error) {
	if len(resources) == 0 {
		return nil, fmt.Errorf("upstreams: expected at least one resource")
	}
	cloned := model.CloneResourceSet(model.ResourceSet{Upstreams: resources}).Upstreams
	sort.Slice(cloned, func(i, j int) bool {
		return cloned[i].ID < cloned[j].ID
	})
	for i := range cloned {
		if strings.TrimSpace(cloned[i].ID) == "" {
			return nil, fmt.Errorf("upstreams[%d].id: must not be empty", i)
		}
		if i > 0 && cloned[i-1].ID == cloned[i].ID {
			return nil, fmt.Errorf("upstreams: duplicate id %q", cloned[i].ID)
		}
	}
	return cloned, nil
}
