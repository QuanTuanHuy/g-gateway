package downstreamtls

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

const (
	// MaxSNINames bounds the total number of configured exact and wildcard SNI
	// names in one runtime snapshot.
	MaxSNINames = 10_000

	// CodeDownstreamTLSRequired reports an absent dynamic downstream TLS policy.
	CodeDownstreamTLSRequired = "DOWNSTREAM_TLS_REQUIRED"
	// CodeDefaultCertificateNotFound reports an empty or unresolved default reference.
	CodeDefaultCertificateNotFound = "DEFAULT_CERTIFICATE_NOT_FOUND"
	// CodeSNIBindingInvalid reports malformed bindings or certificate resources.
	CodeSNIBindingInvalid = "SNI_BINDING_INVALID"
	// CodeSNIBindingConflict reports duplicate canonical exact or wildcard names.
	CodeSNIBindingConflict = "SNI_BINDING_CONFLICT"
	// CodeCertificateHostnameMismatch reports that a binding is not covered by a DNS SAN.
	CodeCertificateHostnameMismatch = "CERTIFICATE_HOSTNAME_MISMATCH"
	// CodeCertificateNotYetValid reports a referenced certificate whose validity has not begun.
	CodeCertificateNotYetValid = "CERTIFICATE_NOT_YET_VALID"
	// CodeCertificateExpired reports a referenced certificate whose validity has ended.
	CodeCertificateExpired = "CERTIFICATE_EXPIRED"
	// CodeSNIIndexLimitExceeded reports a policy with more than MaxSNINames hosts.
	CodeSNIIndexLimitExceeded = "SNI_INDEX_LIMIT_EXCEEDED"
)

var errCertificateUnavailable = errors.New("downstream certificate is unavailable")

// Selection is the bounded certificate-selection class exposed to observers.
type Selection string

const (
	// SelectionExact identifies an exact SNI match.
	SelectionExact Selection = "exact"
	// SelectionWildcard identifies a one-label wildcard SNI match.
	SelectionWildcard Selection = "wildcard"
	// SelectionDefault identifies fallback to the configured default certificate.
	SelectionDefault Selection = "default"
	// SelectionError identifies a certificate-selection failure.
	SelectionError Selection = "error"
)

// Stats contains bounded aggregate information about a compiled selector.
type Stats struct {
	CertificateCount int
	ExactCount       int
	WildcardCount    int
	EarliestExpiry   time.Time
}

// ConfigError describes a stable, bounded downstream TLS validation failure.
type ConfigError struct {
	Code       string
	ResourceID string
	Field      string
	Cause      error
}

// Error returns bounded metadata and deliberately excludes the underlying cause.
func (e *ConfigError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("downstream TLS configuration rejected: code=%s resource=%s field=%s", e.Code, e.ResourceID, e.Field)
}

// Selector is an immutable default, exact, and wildcard certificate index.
type Selector struct {
	defaultCertificate *tls.Certificate
	exact              map[string]*tls.Certificate
	wildcard           map[string]*tls.Certificate
	stats              Stats
}

// SelectingCertificateProvider extends CertificateProvider with a bounded
// certificate-selection classification.
type SelectingCertificateProvider interface {
	CertificateProvider
	Select(*tls.ClientHelloInfo) (*tls.Certificate, Selection, error)
}

// Compile validates policy and material at now and builds an immutable selector.
func Compile(policy *model.DownstreamTLSPolicy, materials []*tlsmaterial.Certificate, now time.Time) (*Selector, error) {
	if policy == nil {
		return nil, configError(CodeDownstreamTLSRequired, "downstream_tls", "downstream_tls", nil)
	}
	hostCount := 0
	for _, binding := range policy.SNIBindings {
		hostCount += len(binding.Hosts)
		if hostCount > MaxSNINames {
			return nil, configError(CodeSNIIndexLimitExceeded, "downstream_tls", "sni_bindings", nil)
		}
	}

	materialByID := make(map[string]*tlsmaterial.Certificate, len(materials))
	for _, material := range materials {
		if material == nil || material.ID() == "" {
			return nil, configError(CodeSNIBindingInvalid, "certificate", "certificates", nil)
		}
		if _, exists := materialByID[material.ID()]; exists {
			return nil, configError(CodeSNIBindingInvalid, material.ID(), "certificates", nil)
		}
		materialByID[material.ID()] = material
	}

	defaultMaterial, exists := materialByID[policy.DefaultCertificateRef]
	if policy.DefaultCertificateRef == "" || !exists {
		return nil, configError(CodeDefaultCertificateNotFound, policy.DefaultCertificateRef, "default_certificate_ref", nil)
	}

	compiled := make(map[string]*tls.Certificate)
	stats := Stats{}
	compileCertificate := func(material *tlsmaterial.Certificate) (*tls.Certificate, error) {
		if certificate, ok := compiled[material.ID()]; ok {
			return certificate, nil
		}
		certificate := material.TLSCertificate()
		if certificate.Leaf == nil || len(certificate.Certificate) == 0 {
			return nil, configError(CodeSNIBindingInvalid, material.ID(), "certificate", nil)
		}
		if now.Before(certificate.Leaf.NotBefore) {
			return nil, configError(CodeCertificateNotYetValid, material.ID(), "not_before", nil)
		}
		if now.After(certificate.Leaf.NotAfter) {
			return nil, configError(CodeCertificateExpired, material.ID(), "not_after", nil)
		}
		compiled[material.ID()] = &certificate
		stats.CertificateCount++
		if stats.EarliestExpiry.IsZero() || certificate.Leaf.NotAfter.Before(stats.EarliestExpiry) {
			stats.EarliestExpiry = certificate.Leaf.NotAfter
		}
		return &certificate, nil
	}

	defaultCertificate, err := compileCertificate(defaultMaterial)
	if err != nil {
		return nil, err
	}
	selector := &Selector{
		defaultCertificate: defaultCertificate,
		exact:              make(map[string]*tls.Certificate),
		wildcard:           make(map[string]*tls.Certificate),
	}
	for _, binding := range policy.SNIBindings {
		if binding.CertificateRef == "" || len(binding.Hosts) == 0 {
			return nil, configError(CodeSNIBindingInvalid, binding.CertificateRef, "sni_bindings", nil)
		}
		material, ok := materialByID[binding.CertificateRef]
		if !ok {
			return nil, configError(CodeSNIBindingInvalid, binding.CertificateRef, "certificate_ref", nil)
		}
		certificate, compileErr := compileCertificate(material)
		if compileErr != nil {
			return nil, compileErr
		}
		for _, rawHost := range binding.Hosts {
			canonical, wildcard, normalizeErr := normalizeBindingHost(rawHost)
			if normalizeErr != nil {
				return nil, configError(CodeSNIBindingInvalid, binding.CertificateRef, "hosts", normalizeErr)
			}
			index := selector.exact
			if wildcard {
				index = selector.wildcard
			}
			if _, duplicate := index[canonical]; duplicate {
				return nil, configError(CodeSNIBindingConflict, binding.CertificateRef, "hosts", nil)
			}
			if !certificateCoversHost(certificate, canonical, wildcard) {
				return nil, configError(CodeCertificateHostnameMismatch, binding.CertificateRef, "hosts", nil)
			}
			index[canonical] = certificate
			if wildcard {
				stats.WildcardCount++
			} else {
				stats.ExactCount++
			}
		}
	}
	selector.stats = stats
	return selector, nil
}

// Select returns the matching immutable certificate and its bounded class.
func (s *Selector) Select(hello *tls.ClientHelloInfo) (*tls.Certificate, Selection, error) {
	if s == nil || s.defaultCertificate == nil {
		return nil, SelectionError, errCertificateUnavailable
	}
	if hello == nil || hello.ServerName == "" {
		return s.defaultCertificate, SelectionDefault, nil
	}
	var scratch [253]byte
	canonical, err := canonicalizeServerName(hello.ServerName, &scratch)
	if err != nil {
		return s.defaultCertificate, SelectionDefault, nil
	}
	if certificate := s.exact[string(canonical)]; certificate != nil {
		return certificate, SelectionExact, nil
	}
	if separator := bytes.IndexByte(canonical, '.'); separator > 0 && separator+1 < len(canonical) {
		if certificate := s.wildcard[string(canonical[separator+1:])]; certificate != nil {
			return certificate, SelectionWildcard, nil
		}
	}
	return s.defaultCertificate, SelectionDefault, nil
}

// GetCertificate implements CertificateProvider.
func (s *Selector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate, _, err := s.Select(hello)
	return certificate, err
}

// Stats returns bounded aggregate selector statistics.
func (s *Selector) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return s.stats
}

func certificateCoversHost(certificate *tls.Certificate, canonical string, wildcard bool) bool {
	if certificate == nil || certificate.Leaf == nil {
		return false
	}
	if !wildcard {
		return certificate.Leaf.VerifyHostname(canonical) == nil
	}
	for _, dnsName := range certificate.Leaf.DNSNames {
		suffix, isWildcard, err := normalizeBindingHost(dnsName)
		if err == nil && isWildcard && suffix == canonical {
			return true
		}
	}
	return false
}

func configError(code, resourceID, field string, cause error) *ConfigError {
	return &ConfigError{Code: code, ResourceID: resourceID, Field: field, Cause: cause}
}
