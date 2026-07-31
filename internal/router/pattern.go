package router

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
)

type segmentKind uint8

const (
	segmentStatic segmentKind = iota
	segmentParameter
	segmentPrefix
	segmentCatchAll
)

type pathSegment struct {
	kind segmentKind
	text string
}

type pathSpecificity struct {
	staticSegments int
	kindRank       int
	segmentCount   int
	patternBytes   int
}

type compiledPathPattern struct {
	raw         string
	segments    []pathSegment
	specificity pathSpecificity
	paramCount  int
}

func compilePathPattern(pattern string) (compiledPathPattern, error) {
	if !strings.HasPrefix(pattern, "/") {
		return compiledPathPattern{}, fmt.Errorf("path pattern %q must be absolute", pattern)
	}
	if strings.ContainsAny(pattern, "?#") {
		return compiledPathPattern{}, fmt.Errorf("path pattern %q must not contain query or fragment", pattern)
	}

	rawSegments := []string(nil)
	if pattern != "/" {
		rawSegments = strings.Split(pattern[1:], "/")
	}
	compiled := compiledPathPattern{
		raw:      pattern,
		segments: make([]pathSegment, 0, len(rawSegments)),
		specificity: pathSpecificity{
			kindRank:     3,
			segmentCount: len(rawSegments),
			patternBytes: len(pattern),
		},
	}
	parameterNames := make(map[string]struct{})
	for i, raw := range rawSegments {
		segment, err := compilePathSegment(raw, i == len(rawSegments)-1)
		if err != nil {
			return compiledPathPattern{}, fmt.Errorf("path pattern %q segment %d: %w", pattern, i, err)
		}
		switch segment.kind {
		case segmentStatic:
			if segment.text != "" {
				compiled.specificity.staticSegments++
			}
		case segmentParameter, segmentCatchAll:
			if _, exists := parameterNames[segment.text]; exists {
				return compiledPathPattern{}, fmt.Errorf("path pattern %q: duplicate parameter name %q", pattern, segment.text)
			}
			parameterNames[segment.text] = struct{}{}
			compiled.paramCount++
			if segment.kind == segmentParameter && compiled.specificity.kindRank > 2 {
				compiled.specificity.kindRank = 2
			}
			if segment.kind == segmentCatchAll {
				compiled.specificity.kindRank = 1
			}
		case segmentPrefix:
			compiled.specificity.kindRank = 1
		}
		compiled.segments = append(compiled.segments, segment)
	}
	return compiled, nil
}

func compilePathSegment(raw string, final bool) (pathSegment, error) {
	switch {
	case raw == "*":
		if !final {
			return pathSegment{}, fmt.Errorf("prefix asterisk must be final")
		}
		return pathSegment{kind: segmentPrefix}, nil
	case strings.HasPrefix(raw, "{*") && strings.HasSuffix(raw, "}") && strings.Count(raw, "{") == 1 && strings.Count(raw, "}") == 1:
		if !final {
			return pathSegment{}, fmt.Errorf("catch-all parameter must be final")
		}
		name := raw[2 : len(raw)-1]
		if !validParameterName(name) {
			return pathSegment{}, fmt.Errorf("invalid catch-all parameter name %q", name)
		}
		return pathSegment{kind: segmentCatchAll, text: name}, nil
	case strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") && strings.Count(raw, "{") == 1 && strings.Count(raw, "}") == 1:
		name := raw[1 : len(raw)-1]
		if !validParameterName(name) {
			return pathSegment{}, fmt.Errorf("invalid parameter name %q", name)
		}
		return pathSegment{kind: segmentParameter, text: name}, nil
	case strings.ContainsRune(raw, '*'):
		return pathSegment{}, fmt.Errorf("mixed asterisk syntax is not allowed")
	case strings.ContainsAny(raw, "{}"):
		return pathSegment{}, fmt.Errorf("braces are allowed only around a complete parameter segment")
	default:
		return pathSegment{kind: segmentStatic, text: raw}, nil
	}
}

func validParameterName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func (p compiledPathPattern) match(path string) (bool, []requestctx.ParamSpan) {
	if !strings.HasPrefix(path, "/") {
		return false, nil
	}
	if len(p.segments) == 0 {
		return path == "/", nil
	}

	var params []requestctx.ParamSpan
	if p.paramCount > 0 {
		params = make([]requestctx.ParamSpan, 0, p.paramCount)
	}
	position := 1
	for i, segment := range p.segments {
		if position > len(path) {
			return false, nil
		}
		if segment.kind == segmentPrefix {
			return true, params
		}
		if segment.kind == segmentCatchAll {
			params = append(params, requestctx.ParamSpan{Name: segment.text, Start: position, End: len(path)})
			return true, params
		}

		end := len(path)
		if offset := strings.IndexByte(path[position:], '/'); offset >= 0 {
			end = position + offset
		}
		switch segment.kind {
		case segmentStatic:
			if path[position:end] != segment.text {
				return false, nil
			}
		case segmentParameter:
			if end == position {
				return false, nil
			}
			params = append(params, requestctx.ParamSpan{Name: segment.text, Start: position, End: end})
		}

		final := i == len(p.segments)-1
		if final {
			return end == len(path), params
		}
		if end == len(path) {
			return false, nil
		}
		position = end + 1
	}
	return false, nil
}

type hostPatternKind uint8

const (
	hostPatternExact hostPatternKind = iota
	hostPatternWildcard
)

type hostSpecificity struct {
	kindRank int
	labels   int
}

type compiledHostPattern struct {
	raw         string
	kind        hostPatternKind
	value       string
	specificity hostSpecificity
}

// NormalizeRequestHost validates and canonicalizes an HTTP host authority. It
// removes a valid port and one trailing DNS dot, lowercases the host, and
// returns an error for empty or malformed authorities.
func NormalizeRequestHost(authority string) (string, error) {
	if authority == "" || strings.TrimSpace(authority) != authority {
		return "", fmt.Errorf("host authority must not be empty or contain surrounding whitespace")
	}

	host := authority
	colonCount := strings.Count(authority, ":")
	if colonCount > 1 && !strings.HasPrefix(authority, "[") {
		if _, err := netip.ParseAddr(authority); err != nil {
			return "", fmt.Errorf("malformed host authority %q: %w", authority, err)
		}
	} else if strings.HasPrefix(authority, "[") || colonCount == 1 {
		parsedHost, port, err := net.SplitHostPort(authority)
		if err != nil {
			return "", fmt.Errorf("malformed host authority %q: %w", authority, err)
		}
		if strings.HasPrefix(authority, "[") {
			if _, err := netip.ParseAddr(parsedHost); err != nil {
				return "", fmt.Errorf("malformed bracketed host authority %q", authority)
			}
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 0 || number > 65535 {
			return "", fmt.Errorf("malformed port in host authority %q", authority)
		}
		host = parsedHost
	}

	host = strings.ToLower(host)
	if strings.HasSuffix(host, ".") {
		host = host[:len(host)-1]
	}
	if host == "" || strings.HasSuffix(host, ".") {
		return "", fmt.Errorf("host authority %q has an empty DNS label", authority)
	}
	return host, nil
}

func compileHostPattern(pattern string) (compiledHostPattern, error) {
	if strings.HasPrefix(pattern, "*.") {
		suffix, err := NormalizeRequestHost(pattern[2:])
		if err != nil {
			return compiledHostPattern{}, fmt.Errorf("wildcard host %q: %w", pattern, err)
		}
		if !validDNSName(suffix) || strings.Count(suffix, ".") < 1 {
			return compiledHostPattern{}, fmt.Errorf("wildcard host %q requires at least two valid DNS labels", pattern)
		}
		return compiledHostPattern{
			raw:   pattern,
			kind:  hostPatternWildcard,
			value: suffix,
			specificity: hostSpecificity{
				kindRank: 1,
				labels:   strings.Count(suffix, ".") + 1,
			},
		}, nil
	}

	host, err := NormalizeRequestHost(pattern)
	if err != nil {
		return compiledHostPattern{}, fmt.Errorf("exact host %q: %w", pattern, err)
	}
	if !validDNSName(host) {
		return compiledHostPattern{}, fmt.Errorf("exact host %q is not a valid DNS name", pattern)
	}
	return compiledHostPattern{
		raw:   pattern,
		kind:  hostPatternExact,
		value: host,
		specificity: hostSpecificity{
			kindRank: 2,
			labels:   strings.Count(host, ".") + 1,
		},
	}, nil
}

func (p compiledHostPattern) match(host string) bool {
	switch p.kind {
	case hostPatternExact:
		return host == p.value
	case hostPatternWildcard:
		suffixStart := len(host) - len(p.value)
		if suffixStart <= 1 || suffixStart > len(host) || host[suffixStart:] != p.value || host[suffixStart-1] != '.' {
			return false
		}
		return !strings.ContainsRune(host[:suffixStart-1], '.')
	default:
		return false
	}
}

func validDNSName(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || !isASCIIAlphaNumeric(label[0]) || !isASCIIAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			if !isASCIIAlphaNumeric(label[i]) && label[i] != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}
