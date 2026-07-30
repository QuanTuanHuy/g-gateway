// Package runtime compiles canonical resources into immutable request
// snapshots and publishes them atomically through a lease-based manager.
//
// A successful lease retains one snapshot revision and its upstream plans
// until Release. Failed builds and stale revisions leave the active snapshot
// unchanged.
package runtime
