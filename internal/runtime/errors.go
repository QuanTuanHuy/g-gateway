package runtime

import (
	"fmt"
	"strings"
)

type BuildStage string

const (
	StageValidate BuildStage = "validate"
	StageResolve  BuildStage = "resolve"
	StagePlugin   BuildStage = "plugin_compile"
	StageRouter   BuildStage = "router_compile"
)

type BuildError struct {
	Code         string
	Stage        BuildStage
	Revision     uint64
	ResourceKind string
	ResourceID   string
	Field        string
	Cause        error
}

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

func (e *BuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
