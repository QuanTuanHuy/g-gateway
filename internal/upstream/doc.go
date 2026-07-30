// Package upstream compiles canonical upstream resources into immutable,
// health-aware selection plans backed by shared transport runtimes.
//
// Registry candidates acquire runtime ownership transactionally. Plans remain
// valid while their owning snapshot lease is held and release resources only
// after retirement and final lease release.
package upstream
