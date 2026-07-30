// Package plugin compiles configured gateway plugins into immutable request
// and response hook chains.
//
// Chains run request hooks in ascending request order and response hooks in
// ascending response order, using plugin names as deterministic tie-breakers.
package plugin
