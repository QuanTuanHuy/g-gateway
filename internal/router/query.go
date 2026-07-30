package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ErrInvalidQuery identifies malformed percent-escaping in a request query.
var ErrInvalidQuery = errors.New("invalid encoded query")

type evaluation struct {
	request    *http.Request
	queryOnce  sync.Once
	query      map[string][]string
	queryError error
}

func newEvaluation(request *http.Request) *evaluation {
	return &evaluation{request: request}
}

func (e *evaluation) queryValues(name string) ([]string, bool, error) {
	e.queryOnce.Do(func() {
		e.query, e.queryError = scanRawQuery(e.request.URL.RawQuery)
	})
	if e.queryError != nil {
		return nil, false, e.queryError
	}
	values, ok := e.query[name]
	return values, ok, nil
}

func scanRawQuery(raw string) (map[string][]string, error) {
	query := make(map[string][]string)
	if raw == "" {
		return query, nil
	}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		keyRaw, valueRaw, found := strings.Cut(pair, "=")
		if !found {
			valueRaw = ""
		}
		key, err := url.QueryUnescape(keyRaw)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %v", ErrInvalidQuery, keyRaw, err)
		}
		value, err := url.QueryUnescape(valueRaw)
		if err != nil {
			return nil, fmt.Errorf("%w: value for %q: %v", ErrInvalidQuery, key, err)
		}
		query[key] = append(query[key], value)
	}
	return query, nil
}
