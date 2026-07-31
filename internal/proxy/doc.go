// Package proxy executes the gateway HTTP request path against leased runtime
// snapshots.
//
// It matches routes, runs compiled plugins, applies bounded retry and timeout
// policy, streams request and response bodies, and maps failures to stable
// gateway responses.
package proxy
