// Package gateway composes configuration, runtime snapshots, proxying,
// telemetry, listeners, and graceful process lifecycle into one data-plane
// instance.
//
// New validates and activates the initial snapshot before Start binds
// listeners. Shutdown removes readiness before draining traffic and releasing
// runtime resources.
package gateway
