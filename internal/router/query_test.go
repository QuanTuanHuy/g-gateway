package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestQueryPredicateSemantics(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"http://gateway/path?tag=a&tag=b&empty=&plus=a+b&Case=Value&case=value",
		nil,
	)
	tests := []struct {
		name  string
		input model.Predicate
		want  bool
	}{
		{
			name:  "equals duplicate value",
			input: model.Predicate{Name: "tag", Operator: model.PredicateEquals, Values: []string{"b"}},
			want:  true,
		},
		{
			name:  "one of duplicate values",
			input: model.Predicate{Name: "tag", Operator: model.PredicateOneOf, Values: []string{"z", "a"}},
			want:  true,
		},
		{
			name:  "empty value exists",
			input: model.Predicate{Name: "empty", Operator: model.PredicateExists},
			want:  true,
		},
		{
			name:  "plus decoded as space",
			input: model.Predicate{Name: "plus", Operator: model.PredicateEquals, Values: []string{"a b"}},
			want:  true,
		},
		{
			name:  "query key is case sensitive",
			input: model.Predicate{Name: "CASE", Operator: model.PredicateExists},
			want:  false,
		},
		{
			name:  "query value is case sensitive",
			input: model.Predicate{Name: "Case", Operator: model.PredicateEquals, Values: []string{"value"}},
			want:  false,
		},
		{
			name:  "not equals rejects duplicate match",
			input: model.Predicate{Name: "tag", Operator: model.PredicateNotEquals, Values: []string{"a"}},
			want:  false,
		},
		{
			name:  "missing not equals",
			input: model.Predicate{Name: "absent", Operator: model.PredicateNotEquals, Values: []string{"a"}},
			want:  false,
		},
		{
			name:  "missing not exists",
			input: model.Predicate{Name: "absent", Operator: model.PredicateNotExists},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := compilePredicates(nil, []model.Predicate{tt.input})
			if err != nil {
				t.Fatal(err)
			}
			got, err := compiled.evaluate(newEvaluation(request))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryInvalidEscapeReturnsSentinel(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path", nil)
	request.URL.RawQuery = "tag=%zz"
	compiled, err := compilePredicates(nil, []model.Predicate{{
		Name:     "tag",
		Operator: model.PredicateExists,
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = compiled.evaluate(newEvaluation(request))
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("evaluate() error = %v, want ErrInvalidQuery", err)
	}
}

func TestQueryParsingIsLazyForHeaderOnlyPredicates(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path", nil)
	request.URL.RawQuery = "tag=%zz"
	request.Header.Set("X-Role", "reader")
	compiled, err := compilePredicates([]model.Predicate{{
		Name:     "X-Role",
		Operator: model.PredicateEquals,
		Values:   []string{"reader"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := compiled.evaluate(newEvaluation(request))
	if err != nil || !got {
		t.Fatalf("evaluate() = %v, %v", got, err)
	}
}

func TestQueryScannerPreservesKeyWithoutEquals(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path?flag", nil)
	compiled, err := compilePredicates(nil, []model.Predicate{{
		Name:     "flag",
		Operator: model.PredicateEquals,
		Values:   []string{""},
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := compiled.evaluate(newEvaluation(request))
	if err != nil || !got {
		t.Fatalf("evaluate() = %v, %v", got, err)
	}
}
