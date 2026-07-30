// Package telemetry exposes bounded gateway health, readiness, request,
// runtime, and upstream metrics through the private admin handler.
//
// Metric label sets are fixed by construction; callers must not introduce
// resource IDs, endpoints, revisions, or error text as labels.
package telemetry
