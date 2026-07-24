package router

import (
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

func referenceMatch(t *testing.T, specs []RouteSpec, request *http.Request) Result {
	t.Helper()
	normalizedHost, err := NormalizeRequestHost(request.Host)
	if err != nil {
		t.Fatal(err)
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		t.Fatal(err)
	}

	var matches []referenceCandidate
	allow := make(map[string]struct{})
	for _, spec := range specs {
		hostRank, hostMatches := referenceHostMatch(t, spec.Match.Hosts, normalizedHost)
		if !hostMatches {
			continue
		}
		path, err := compilePathPattern(spec.Match.Path)
		if err != nil {
			t.Fatal(err)
		}
		pathMatches, params := path.match(request.URL.Path)
		if !pathMatches || !referencePredicatesMatch(spec.Match, request.Header, query) {
			continue
		}

		methodMatches := false
		for _, configured := range spec.Match.Methods {
			method := strings.ToUpper(configured)
			if method == request.Method {
				methodMatches = true
			}
		}
		if !methodMatches {
			for _, configured := range spec.Match.Methods {
				allow[strings.ToUpper(configured)] = struct{}{}
			}
			continue
		}
		matches = append(matches, referenceCandidate{
			index:  spec.Index,
			params: params,
			key: referencePrecedence{
				priority:       spec.Priority,
				hostRank:       hostRank,
				pathKind:       path.specificity.kindRank,
				staticSegments: path.specificity.staticSegments,
				patternBytes:   path.specificity.patternBytes,
				predicateCount: len(spec.Match.Headers) + len(spec.Match.Queries),
				routeID:        spec.ID,
			},
		})
	}

	if len(matches) > 0 {
		sort.SliceStable(matches, func(i, j int) bool {
			return referencePrecedenceBetter(matches[i].key, matches[j].key)
		})
		return Result{Found: true, RouteIndex: matches[0].index, Params: matches[0].params}
	}
	if len(allow) > 0 {
		values := make([]string, 0, len(allow))
		for method := range allow {
			values = append(values, method)
		}
		sort.Strings(values)
		return Result{MethodNotAllowed: true, Allow: values}
	}
	return Result{}
}

type referenceCandidate struct {
	index  int
	params []requestctx.ParamSpan
	key    referencePrecedence
}

type referencePrecedence struct {
	priority       int
	hostRank       int
	pathKind       int
	staticSegments int
	patternBytes   int
	predicateCount int
	routeID        string
}

func referencePrecedenceBetter(left, right referencePrecedence) bool {
	leftValues := [6]int{
		left.priority,
		left.hostRank,
		left.pathKind,
		left.staticSegments,
		left.patternBytes,
		left.predicateCount,
	}
	rightValues := [6]int{
		right.priority,
		right.hostRank,
		right.pathKind,
		right.staticSegments,
		right.patternBytes,
		right.predicateCount,
	}
	for i := range leftValues {
		if leftValues[i] != rightValues[i] {
			return leftValues[i] > rightValues[i]
		}
	}
	return left.routeID < right.routeID
}

func referenceHostMatch(t *testing.T, patterns []string, host string) (int, bool) {
	t.Helper()
	if len(patterns) == 0 {
		return 0, true
	}
	bestRank := -1
	for _, raw := range patterns {
		pattern, err := compileHostPattern(raw)
		if err != nil {
			t.Fatal(err)
		}
		if pattern.match(host) && pattern.specificity.kindRank > bestRank {
			bestRank = pattern.specificity.kindRank
		}
	}
	return bestRank, bestRank >= 0
}

func referencePredicatesMatch(match model.RouteMatch, headers http.Header, query url.Values) bool {
	for _, predicate := range match.Headers {
		values, present := referenceHeaderValues(headers, predicate.Name)
		if !referencePredicateMatch(predicate, values, present) {
			return false
		}
	}
	for _, predicate := range match.Queries {
		values, present := query[predicate.Name]
		if !referencePredicateMatch(predicate, values, present) {
			return false
		}
	}
	return true
}

func referenceHeaderValues(headers http.Header, name string) ([]string, bool) {
	name = textproto.CanonicalMIMEHeaderKey(name)
	var values []string
	present := false
	for key, current := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
			present = true
		}
	}
	return values, present
}

func referencePredicateMatch(predicate model.Predicate, values []string, present bool) bool {
	switch predicate.Operator {
	case model.PredicateExists:
		return present
	case model.PredicateNotExists:
		return !present
	case model.PredicateEquals, model.PredicateOneOf:
		if !present {
			return false
		}
		for _, current := range values {
			for _, expected := range predicate.Values {
				if current == expected {
					return true
				}
			}
		}
		return false
	case model.PredicateNotEquals:
		if !present {
			return false
		}
		for _, current := range values {
			for _, rejected := range predicate.Values {
				if current == rejected {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}
