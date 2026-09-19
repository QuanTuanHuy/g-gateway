package downstreamtls

import (
	"crypto/tls"
	"errors"
	"fmt"
)

// CertificateProvider supplies an immutable certificate for one TLS client
// hello.
type CertificateProvider interface {
	// GetCertificate returns the immutable certificate for one TLS client hello.
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// StaticProvider returns one certificate independently of SNI.
type StaticProvider struct {
	certificate tls.Certificate
}

// LoadStatic loads and owns one certificate and private-key pair.
func LoadStatic(certificateFile, privateKeyFile string) (*StaticProvider, error) {
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load downstream certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("downstream certificate is unavailable")
	}
	return &StaticProvider{certificate: certificate}, nil
}

// GetCertificate returns the provider-owned certificate. Callers must treat
// the returned value as immutable.
func (p *StaticProvider) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if p == nil || len(p.certificate.Certificate) == 0 {
		return nil, errors.New("downstream certificate is unavailable")
	}
	return &p.certificate, nil
}

// Select returns the static certificate with the default selection class.
func (p *StaticProvider) Select(hello *tls.ClientHelloInfo) (*tls.Certificate, Selection, error) {
	certificate, err := p.GetCertificate(hello)
	if err != nil {
		return nil, SelectionError, err
	}
	return certificate, SelectionDefault, nil
}
