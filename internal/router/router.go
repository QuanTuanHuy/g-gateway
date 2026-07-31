package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

// RouteSpec associates a canonical route match with the caller's stable route
// index and explicit priority.
type RouteSpec struct {
	// Index is returned in Result when this route wins.
	Index int
	// ID uniquely identifies the route and is the final deterministic
	// precedence tie-breaker.
	ID string
	// Priority is compared before compiled host, path, and predicate
	// specificity.
	Priority int
	// Match contains the canonical HTTP match expression to compile.
	Match model.RouteMatch
}

// Router is an immutable compiled route index safe for concurrent matching.
type Router struct {
	exactHosts    map[string]*pathNode
	wildcardHosts map[string]*pathNode
	hostless      *pathNode
}

type pathNode struct {
	static     map[string]*pathNode
	parameter  *pathNode
	terminal   []candidate
	prefix     []candidate
	catchAll   []candidate
	upper      precedenceKey
	upperValid bool
}

type candidate struct {
	routeIndex int
	routeID    string
	priority   int
	hostRank   int
	path       compiledPathPattern
	methods    methodSet
	predicates compiledPredicateSet
	key        precedenceKey
}

type precedenceKey struct {
	priority       int
	hostRank       int
	pathKind       int
	staticSegments int
	patternBytes   int
	predicateCount int
	routeID        string
}

// Result describes a successful route match, a method-only mismatch, or no
// match. Params offsets refer to the request URL path passed to Match.
type Result struct {
	// Found reports that RouteIndex and Params identify the winning route.
	Found bool
	// MethodNotAllowed reports that path, host, and predicates matched but no
	// route accepted the request method.
	MethodNotAllowed bool
	// RouteIndex is the caller-provided index of the winning route.
	RouteIndex int
	// Params contains byte spans captured from the matched request path.
	Params []requestctx.ParamSpan
	// Allow is the sorted, deduplicated method set for a method-only mismatch.
	Allow []string
}

// Compile builds an immutable deterministic Router from routes. It rejects
// empty input, empty or duplicate IDs, invalid match expressions, and duplicate
// canonical priority-plus-match expressions.
func Compile(routes []RouteSpec) (*Router, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("routes must not be empty")
	}
	compiled := &Router{
		exactHosts:    make(map[string]*pathNode),
		wildcardHosts: make(map[string]*pathNode),
		hostless:      &pathNode{},
	}
	routeIDs := make(map[string]struct{}, len(routes))
	signatures := make(map[string]string, len(routes))

	for i, route := range routes {
		if strings.TrimSpace(route.ID) == "" {
			return nil, fmt.Errorf("route %d: ID must not be empty", i)
		}
		if _, duplicate := routeIDs[route.ID]; duplicate {
			return nil, fmt.Errorf("duplicate route ID %q", route.ID)
		}
		routeIDs[route.ID] = struct{}{}

		path, err := compilePathPattern(route.Match.Path)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		methods, err := compileMethods(route.Match.Methods)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		predicates, err := compilePredicates(route.Match.Headers, route.Match.Queries)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}
		hosts, err := compileRouteHosts(route.Match.Hosts)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.ID, err)
		}

		signature, err := routeSignature(route.Priority, path.raw, hosts, methods, predicates)
		if err != nil {
			return nil, fmt.Errorf("route %q signature: %w", route.ID, err)
		}
		if previous, duplicate := signatures[signature]; duplicate {
			return nil, fmt.Errorf("routes %q and %q have duplicate priority and match expression", previous, route.ID)
		}
		signatures[signature] = route.ID

		base := candidate{
			routeIndex: route.Index,
			routeID:    route.ID,
			priority:   route.Priority,
			path:       path,
			methods:    methods,
			predicates: predicates,
		}
		if len(hosts) == 0 {
			base.hostRank = 0
			base.key = makePrecedenceKey(base)
			insertCandidate(compiled.hostless, base)
			continue
		}
		for _, host := range hosts {
			current := base
			current.hostRank = host.specificity.kindRank
			current.key = makePrecedenceKey(current)
			var tree *pathNode
			switch host.kind {
			case hostPatternExact:
				tree = compiled.exactHosts[host.value]
				if tree == nil {
					tree = &pathNode{}
					compiled.exactHosts[host.value] = tree
				}
			case hostPatternWildcard:
				tree = compiled.wildcardHosts[host.value]
				if tree == nil {
					tree = &pathNode{}
					compiled.wildcardHosts[host.value] = tree
				}
			}
			insertCandidate(tree, current)
		}
	}

	finalizeNode(compiled.hostless)
	for _, tree := range compiled.exactHosts {
		finalizeNode(tree)
	}
	for _, tree := range compiled.wildcardHosts {
		finalizeNode(tree)
	}
	return compiled, nil
}

func compileRouteHosts(raw []string) ([]compiledHostPattern, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	hosts := make([]compiledHostPattern, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, pattern := range raw {
		host, err := compileHostPattern(pattern)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d:%s", host.kind, host.value)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate normalized host pattern %q", pattern)
		}
		seen[key] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func insertCandidate(root *pathNode, value candidate) {
	node := root
	for _, segment := range value.path.segments {
		switch segment.kind {
		case segmentPrefix:
			node.prefix = append(node.prefix, value)
			return
		case segmentCatchAll:
			node.catchAll = append(node.catchAll, value)
			return
		case segmentParameter:
			if node.parameter == nil {
				node.parameter = &pathNode{}
			}
			node = node.parameter
		case segmentStatic:
			if node.static == nil {
				node.static = make(map[string]*pathNode)
			}
			child := node.static[segment.text]
			if child == nil {
				child = &pathNode{}
				node.static[segment.text] = child
			}
			node = child
		}
	}
	node.terminal = append(node.terminal, value)
}

func finalizeNode(node *pathNode) {
	if node == nil {
		return
	}
	sortCandidates(node.terminal)
	sortCandidates(node.prefix)
	sortCandidates(node.catchAll)
	for _, values := range [][]candidate{node.terminal, node.prefix, node.catchAll} {
		for _, value := range values {
			node.includeUpper(value.key)
		}
	}
	if node.parameter != nil {
		finalizeNode(node.parameter)
		if node.parameter.upperValid {
			node.includeUpper(node.parameter.upper)
		}
	}
	for _, child := range node.static {
		finalizeNode(child)
		if child.upperValid {
			node.includeUpper(child.upper)
		}
	}
}

func sortCandidates(values []candidate) {
	sort.Slice(values, func(i, j int) bool {
		return comparePrecedence(values[i].key, values[j].key) > 0
	})
}

func (n *pathNode) includeUpper(key precedenceKey) {
	if !n.upperValid || comparePrecedence(key, n.upper) > 0 {
		n.upper = key
		n.upperValid = true
	}
}

func makePrecedenceKey(value candidate) precedenceKey {
	return precedenceKey{
		priority:       value.priority,
		hostRank:       value.hostRank,
		pathKind:       value.path.specificity.kindRank,
		staticSegments: value.path.specificity.staticSegments,
		patternBytes:   value.path.specificity.patternBytes,
		predicateCount: len(value.predicates),
		routeID:        value.routeID,
	}
}

func comparePrecedence(left, right precedenceKey) int {
	for _, pair := range [][2]int{
		{left.priority, right.priority},
		{left.hostRank, right.hostRank},
		{left.pathKind, right.pathKind},
		{left.staticSegments, right.staticSegments},
		{left.patternBytes, right.patternBytes},
		{left.predicateCount, right.predicateCount},
	} {
		if pair[0] > pair[1] {
			return 1
		}
		if pair[0] < pair[1] {
			return -1
		}
	}
	if left.routeID < right.routeID {
		return 1
	}
	if left.routeID > right.routeID {
		return -1
	}
	return 0
}

// Match returns the highest-precedence route for request. It distinguishes a
// method-only mismatch from no match and may return ErrInvalidQuery for
// malformed query escaping.
func (r *Router) Match(request *http.Request) (Result, error) {
	path := request.URL.Path
	if path == "" {
		path = "/"
	}
	state := matcherState{
		request: request,
		path:    path,
	}

	if len(r.exactHosts) > 0 || len(r.wildcardHosts) > 0 {
		host, err := NormalizeRequestHost(request.Host)
		if err != nil {
			return Result{}, err
		}
		if tree := r.exactHosts[host]; tree != nil {
			if err := state.visit(tree, 1); err != nil {
				return Result{}, err
			}
		}
		if separator := strings.IndexByte(host, '.'); separator > 0 {
			if tree := r.wildcardHosts[host[separator+1:]]; tree != nil {
				if err := state.visit(tree, 1); err != nil {
					return Result{}, err
				}
			}
		}
	}
	if err := state.visit(r.hostless, 1); err != nil {
		return Result{}, err
	}

	if state.found {
		return Result{
			Found:      true,
			RouteIndex: state.routeIndex,
			Params:     state.params,
		}, nil
	}
	if len(state.allow) > 0 {
		allow := make([]string, 0, len(state.allow))
		for method := range state.allow {
			allow = append(allow, method)
		}
		sort.Strings(allow)
		return Result{MethodNotAllowed: true, Allow: allow}, nil
	}
	return Result{}, nil
}

type matcherState struct {
	request    *http.Request
	path       string
	eval       *evaluation
	found      bool
	best       precedenceKey
	routeIndex int
	params     []requestctx.ParamSpan
	allow      map[string]struct{}
}

func (s *matcherState) visit(node *pathNode, position int) error {
	if node == nil || !node.upperValid {
		return nil
	}
	if s.found && comparePrecedence(node.upper, s.best) <= 0 {
		return nil
	}
	if err := s.consider(node.terminal); err != nil {
		return err
	}
	if err := s.consider(node.prefix); err != nil {
		return err
	}
	if err := s.consider(node.catchAll); err != nil {
		return err
	}

	if position > len(s.path) {
		return nil
	}
	end := len(s.path)
	if offset := strings.IndexByte(s.path[position:], '/'); offset >= 0 {
		end = position + offset
	}
	nextPosition := len(s.path) + 1
	if end < len(s.path) {
		nextPosition = end + 1
	}

	staticChild := node.static[s.path[position:end]]
	var parameterChild *pathNode
	if end > position {
		parameterChild = node.parameter
	}
	if staticChild != nil && parameterChild != nil &&
		compareNodeUpper(parameterChild, staticChild) > 0 {
		if err := s.visit(parameterChild, nextPosition); err != nil {
			return err
		}
		return s.visit(staticChild, nextPosition)
	}
	if err := s.visit(staticChild, nextPosition); err != nil {
		return err
	}
	return s.visit(parameterChild, nextPosition)
}

func compareNodeUpper(left, right *pathNode) int {
	switch {
	case left == nil || !left.upperValid:
		return -1
	case right == nil || !right.upperValid:
		return 1
	default:
		return comparePrecedence(left.upper, right.upper)
	}
}

func (s *matcherState) consider(values []candidate) error {
	for _, value := range values {
		if s.found && comparePrecedence(value.key, s.best) <= 0 {
			break
		}
		pathMatches, params := value.path.match(s.path)
		if !pathMatches {
			continue
		}
		predicatesMatch := true
		if len(value.predicates) > 0 {
			if s.eval == nil {
				s.eval = newEvaluation(s.request)
			}
			var err error
			predicatesMatch, err = value.predicates.evaluate(s.eval)
			if err != nil {
				return err
			}
		}
		if !predicatesMatch {
			continue
		}
		if value.methods.contains(s.request.Method) {
			s.found = true
			s.best = value.key
			s.routeIndex = value.routeIndex
			s.params = params
			continue
		}
		s.addAllow(value.methods)
	}
	return nil
}

func (s *matcherState) addAllow(methods methodSet) {
	if s.allow == nil {
		s.allow = make(map[string]struct{})
	}
	for _, method := range standardMethods {
		if methods.contains(method) {
			s.allow[method] = struct{}{}
		}
	}
	for method := range methods.custom {
		s.allow[method] = struct{}{}
	}
}

type canonicalRouteSignature struct {
	Priority   int                  `json:"priority"`
	Path       string               `json:"path"`
	Hosts      []string             `json:"hosts"`
	Methods    []string             `json:"methods"`
	Predicates []canonicalPredicate `json:"predicates"`
}

type canonicalPredicate struct {
	Kind     fieldKind               `json:"kind"`
	Name     string                  `json:"name"`
	Operator model.PredicateOperator `json:"operator"`
	Values   []string                `json:"values"`
}

func routeSignature(
	priority int,
	path string,
	hosts []compiledHostPattern,
	methods methodSet,
	predicates compiledPredicateSet,
) (string, error) {
	canonical := canonicalRouteSignature{
		Priority: priority,
		Path:     path,
		Hosts:    make([]string, 0, len(hosts)),
		Methods:  methods.sortedValues(),
	}
	for _, host := range hosts {
		prefix := "="
		if host.kind == hostPatternWildcard {
			prefix = "*."
		}
		canonical.Hosts = append(canonical.Hosts, prefix+host.value)
	}
	sort.Strings(canonical.Hosts)
	canonical.Predicates = make([]canonicalPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		values := make([]string, 0, len(predicate.values))
		for value := range predicate.values {
			values = append(values, value)
		}
		sort.Strings(values)
		canonical.Predicates = append(canonical.Predicates, canonicalPredicate{
			Kind:     predicate.kind,
			Name:     predicate.name,
			Operator: predicate.operator,
			Values:   values,
		})
	}
	sort.Slice(canonical.Predicates, func(i, j int) bool {
		left, _ := json.Marshal(canonical.Predicates[i])
		right, _ := json.Marshal(canonical.Predicates[j])
		return string(left) < string(right)
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
