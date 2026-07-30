// Package telemetry exposes bounded gateway health, readiness, request,
// runtime, and upstream metrics through the private admin handler.
//
// Metric names and label names are fixed by construction. Request metrics use
// the observed HTTP method and configuration-bounded route and upstream IDs;
// lifecycle and resilience metrics avoid raw endpoints, revisions, error text,
// request paths, and request hosts as label values.
package telemetry
