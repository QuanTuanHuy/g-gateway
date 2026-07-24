package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestPredicateHeaderSemantics(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path", nil)
	request.Header["X-Role"] = []string{"reader", "writer"}
	request.Header["X-Empty"] = []string{""}

	tests := []struct {
		name  string
		input model.Predicate
		want  bool
	}{
		{
			name:  "equals any value case insensitive name",
			input: model.Predicate{Name: "x-role", Operator: model.PredicateEquals, Values: []string{"writer"}},
			want:  true,
		},
		{
			name:  "equals is byte sensitive",
			input: model.Predicate{Name: "X-Role", Operator: model.PredicateEquals, Values: []string{"Writer"}},
			want:  false,
		},
		{
			name:  "not equals requires none",
			input: model.Predicate{Name: "X-Role", Operator: model.PredicateNotEquals, Values: []string{"admin"}},
			want:  true,
		},
		{
			name:  "not equals rejects one match",
			input: model.Predicate{Name: "X-Role", Operator: model.PredicateNotEquals, Values: []string{"reader"}},
			want:  false,
		},
		{
			name:  "one of",
			input: model.Predicate{Name: "X-Role", Operator: model.PredicateOneOf, Values: []string{"admin", "writer"}},
			want:  true,
		},
		{
			name:  "empty value still exists",
			input: model.Predicate{Name: "X-Empty", Operator: model.PredicateExists},
			want:  true,
		},
		{
			name:  "missing exists",
			input: model.Predicate{Name: "X-Missing", Operator: model.PredicateExists},
			want:  false,
		},
		{
			name:  "missing not exists",
			input: model.Predicate{Name: "X-Missing", Operator: model.PredicateNotExists},
			want:  true,
		},
		{
			name:  "missing equals",
			input: model.Predicate{Name: "X-Missing", Operator: model.PredicateEquals, Values: []string{"value"}},
			want:  false,
		},
		{
			name:  "missing not equals",
			input: model.Predicate{Name: "X-Missing", Operator: model.PredicateNotEquals, Values: []string{"value"}},
			want:  false,
		},
		{
			name:  "missing one of",
			input: model.Predicate{Name: "X-Missing", Operator: model.PredicateOneOf, Values: []string{"value"}},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := compilePredicates([]model.Predicate{tt.input}, nil)
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

func TestPredicateCompilerRejectsInvalidDefinitions(t *testing.T) {
	tests := []model.Predicate{
		{Name: "", Operator: model.PredicateExists},
		{Name: "role", Operator: "regex", Values: []string{"admin"}},
		{Name: "role", Operator: model.PredicateExists, Values: []string{"admin"}},
		{Name: "role", Operator: model.PredicateNotExists, Values: []string{"admin"}},
		{Name: "role", Operator: model.PredicateEquals},
		{Name: "role", Operator: model.PredicateNotEquals, Values: []string{"a", "b"}},
		{Name: "role", Operator: model.PredicateOneOf},
	}
	for _, predicate := range tests {
		t.Run(string(predicate.Operator)+"_"+predicate.Name, func(t *testing.T) {
			if _, err := compilePredicates([]model.Predicate{predicate}, nil); err == nil {
				t.Fatalf("compilePredicates(%+v) unexpectedly succeeded", predicate)
			}
		})
	}
}

func TestPredicateSetUsesANDSemantics(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path?tenant=acme", nil)
	request.Header.Set("X-Role", "reader")
	compiled, err := compilePredicates(
		[]model.Predicate{{Name: "X-Role", Operator: model.PredicateEquals, Values: []string{"reader"}}},
		[]model.Predicate{{Name: "tenant", Operator: model.PredicateEquals, Values: []string{"globex"}}},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := compiled.evaluate(newEvaluation(request))
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("evaluate() = true when one predicate failed")
	}
}

func TestMethodSetSemantics(t *testing.T) {
	methods, err := compileMethods([]string{"get", "POST", "purge"})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST", "PURGE"} {
		if !methods.contains(method) {
			t.Fatalf("contains(%q) = false", method)
		}
	}
	for _, method := range []string{"HEAD", "get", "purge"} {
		if methods.contains(method) {
			t.Fatalf("contains(%q) = true", method)
		}
	}
	if got, want := methods.sortedValues(), []string{"GET", "POST", "PURGE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedValues() = %v, want %v", got, want)
	}
}

func TestMethodSetRejectsInvalidOrDuplicateMethods(t *testing.T) {
	tests := [][]string{
		nil,
		{"GET", "get"},
		{"BAD METHOD"},
		{strings.Repeat("A", 1) + "\n"},
	}
	for _, methods := range tests {
		if _, err := compileMethods(methods); err == nil {
			t.Fatalf("compileMethods(%q) unexpectedly succeeded", methods)
		}
	}
}
