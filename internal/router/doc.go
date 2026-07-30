// Package router compiles canonical route match expressions into an immutable,
// deterministic HTTP request router.
//
// Matching performs no configuration lookup. Route priority and compiled
// specificity, rather than declaration order, determine the winning route.
package router
