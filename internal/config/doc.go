// Package config loads, strictly decodes, and validates versioned gateway
// configuration into bootstrap settings and canonical model resources.
//
// Decoding rejects unknown fields and multiple YAML documents. Successful
// results are ready for runtime compilation but remain caller-owned values.
package config
