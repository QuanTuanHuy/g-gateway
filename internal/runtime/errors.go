package runtime

import (
	"fmt"
	"strings"
)

// BuildStage identifies the phase that rejected a snapshot build.
type BuildStage string

const (
	// StageValidate covers revision, resource, and manager precondition
	// validation.
	StageValidate BuildStage = "validate"
	// StageResolve covers cross-resource and prepared-upstream resolution.
	StageResolve BuildStage = "resolve"
	// StagePlugin covers plugin-chain compilation.
	StagePlugin BuildStage = "plugin_compile"
	// StageRouter covers deterministic router compilation.
	StageRouter BuildStage = "router_compile"
)

// BuildError is a stable coded snapshot-build error with optional resource and
// field context.
type BuildError struct {
	// Code is the stable machine-readable failure category.
	Code string
	// Stage identifies the build phase that failed.
	Stage BuildStage
	// Revision is the candidate revision.
	Revision uint64
	// ResourceKind identifies the affected resource kind, when applicable.
	ResourceKind string
	// ResourceID identifies the affected resource, when applicable.
	ResourceID string
	// Field is the canonical path of the invalid or unresolved value.
	Field string
	// Cause contains the underlying validation or compilation error.
	Cause error
}

// Error formats the stable code and available stage, revision, resource, field,
// and cause context. It returns "<nil>" for a nil receiver.
func (e *BuildError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var details []string
	if e.Stage != "" {
		details = append(details, "stage="+string(e.Stage))
	}
	if e.Revision != 0 {
		details = append(details, fmt.Sprintf("revision=%d", e.Revision))
	}
	if e.ResourceKind != "" {
		details = append(details, "kind="+e.ResourceKind)
	}
	if e.ResourceID != "" {
		details = append(details, "id="+e.ResourceID)
	}
	if e.Field != "" {
		details = append(details, "field="+e.Field)
	}
	message := e.Code
	if len(details) > 0 {
		message += " (" + strings.Join(details, ",") + ")"
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

// Unwrap returns the underlying build cause, or nil for a nil receiver.
func (e *BuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
