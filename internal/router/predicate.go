package router

import (
	"fmt"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"unicode"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type fieldKind uint8

const (
	fieldHeader fieldKind = iota
	fieldQuery
)

type compiledPredicate struct {
	kind     fieldKind
	name     string
	operator model.PredicateOperator
	values   map[string]struct{}
}

type compiledPredicateSet []compiledPredicate

func compilePredicates(headers, queries []model.Predicate) (compiledPredicateSet, error) {
	compiled := make(compiledPredicateSet, 0, len(headers)+len(queries))
	for i, predicate := range headers {
		instruction, err := compilePredicate(fieldHeader, predicate)
		if err != nil {
			return nil, fmt.Errorf("header predicate %d: %w", i, err)
		}
		compiled = append(compiled, instruction)
	}
	for i, predicate := range queries {
		instruction, err := compilePredicate(fieldQuery, predicate)
		if err != nil {
			return nil, fmt.Errorf("query predicate %d: %w", i, err)
		}
		compiled = append(compiled, instruction)
	}
	return compiled, nil
}

func compilePredicate(kind fieldKind, predicate model.Predicate) (compiledPredicate, error) {
	if strings.TrimSpace(predicate.Name) == "" {
		return compiledPredicate{}, fmt.Errorf("name must not be empty")
	}
	switch predicate.Operator {
	case model.PredicateExists, model.PredicateNotExists:
		if len(predicate.Values) != 0 {
			return compiledPredicate{}, fmt.Errorf("operator %q requires no values", predicate.Operator)
		}
	case model.PredicateEquals, model.PredicateNotEquals:
		if len(predicate.Values) != 1 {
			return compiledPredicate{}, fmt.Errorf("operator %q requires exactly one value", predicate.Operator)
		}
	case model.PredicateOneOf:
		if len(predicate.Values) == 0 {
			return compiledPredicate{}, fmt.Errorf("operator %q requires at least one value", predicate.Operator)
		}
	default:
		return compiledPredicate{}, fmt.Errorf("unsupported operator %q", predicate.Operator)
	}

	name := predicate.Name
	if kind == fieldHeader {
		name = textproto.CanonicalMIMEHeaderKey(name)
		if name == "" {
			return compiledPredicate{}, fmt.Errorf("invalid header name %q", predicate.Name)
		}
	}
	compiled := compiledPredicate{
		kind:     kind,
		name:     name,
		operator: predicate.Operator,
	}
	if len(predicate.Values) > 0 {
		compiled.values = make(map[string]struct{}, len(predicate.Values))
		for _, value := range predicate.Values {
			compiled.values[value] = struct{}{}
		}
	}
	return compiled, nil
}

func (p compiledPredicateSet) evaluate(e *evaluation) (bool, error) {
	for _, predicate := range p {
		var (
			values  []string
			present bool
			err     error
		)
		switch predicate.kind {
		case fieldHeader:
			values, present = headerValues(e.request.Header, predicate.name)
		case fieldQuery:
			values, present, err = e.queryValues(predicate.name)
			if err != nil {
				return false, err
			}
		}

		matched := false
		switch predicate.operator {
		case model.PredicateExists:
			matched = present
		case model.PredicateNotExists:
			matched = !present
		case model.PredicateEquals, model.PredicateOneOf:
			matched = present && anyValueMatches(values, predicate.values)
		case model.PredicateNotEquals:
			matched = present && !anyValueMatches(values, predicate.values)
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

func headerValues(header http.Header, name string) ([]string, bool) {
	values, present := header[name]
	for key, candidate := range header {
		if key == name || !strings.EqualFold(key, name) {
			continue
		}
		if !present {
			values = candidate
			present = true
			continue
		}
		combined := make([]string, 0, len(values)+len(candidate))
		combined = append(combined, values...)
		combined = append(combined, candidate...)
		values = combined
	}
	return values, present
}

func anyValueMatches(values []string, expected map[string]struct{}) bool {
	for _, value := range values {
		if _, ok := expected[value]; ok {
			return true
		}
	}
	return false
}

type methodSet struct {
	standard uint16
	custom   map[string]struct{}
}

var standardMethods = []string{
	http.MethodConnect,
	http.MethodDelete,
	http.MethodGet,
	http.MethodHead,
	http.MethodOptions,
	http.MethodPatch,
	http.MethodPost,
	http.MethodPut,
	http.MethodTrace,
}

func compileMethods(methods []string) (methodSet, error) {
	if len(methods) == 0 {
		return methodSet{}, fmt.Errorf("methods must not be empty")
	}
	var compiled methodSet
	seen := make(map[string]struct{}, len(methods))
	for i, configured := range methods {
		if !validHTTPToken(configured) {
			return methodSet{}, fmt.Errorf("method %d: invalid HTTP token %q", i, configured)
		}
		method := strings.ToUpper(configured)
		if _, duplicate := seen[method]; duplicate {
			return methodSet{}, fmt.Errorf("duplicate method %q", method)
		}
		seen[method] = struct{}{}
		if bit, standard := standardMethodBit(method); standard {
			compiled.standard |= bit
			continue
		}
		if compiled.custom == nil {
			compiled.custom = make(map[string]struct{})
		}
		compiled.custom[method] = struct{}{}
	}
	return compiled, nil
}

func (m methodSet) contains(method string) bool {
	if bit, standard := standardMethodBit(method); standard {
		return m.standard&bit != 0
	}
	_, ok := m.custom[method]
	return ok
}

func (m methodSet) sortedValues() []string {
	values := make([]string, 0, len(standardMethods)+len(m.custom))
	for _, method := range standardMethods {
		if m.contains(method) {
			values = append(values, method)
		}
	}
	for method := range m.custom {
		values = append(values, method)
	}
	sort.Strings(values)
	return values
}

func standardMethodBit(method string) (uint16, bool) {
	switch method {
	case http.MethodConnect:
		return 1 << 0, true
	case http.MethodDelete:
		return 1 << 1, true
	case http.MethodGet:
		return 1 << 2, true
	case http.MethodHead:
		return 1 << 3, true
	case http.MethodOptions:
		return 1 << 4, true
	case http.MethodPatch:
		return 1 << 5, true
	case http.MethodPost:
		return 1 << 6, true
	case http.MethodPut:
		return 1 << 7, true
	case http.MethodTrace:
		return 1 << 8, true
	default:
		return 0, false
	}
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}
