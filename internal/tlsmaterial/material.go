// Package tlsmaterial parses and owns immutable TLS certificate and trust
// material used by transport generations.
package tlsmaterial

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// Fingerprint is a domain-separated SHA-256 identity for public TLS material.
type Fingerprint [32]byte

// Certificate contains one immutable ordered certificate chain and its
// matching parsed private key.
type Certificate struct {
	id          string
	certificate tls.Certificate
	fingerprint Fingerprint
}

// TrustBundle contains an immutable de-duplicated set of CA certificates.
type TrustBundle struct {
	id           string
	certificates []*x509.Certificate
	fingerprint  Fingerprint
}

// NewCertificate parses one PEM certificate chain and matching private key.
// The input byte slices are not retained.
func NewCertificate(id string, certificatePEM, privateKeyPEM []byte) (*Certificate, error) {
	if id == "" || strings.TrimSpace(id) != id {
		return nil, fmt.Errorf("certificate id must be non-empty without surrounding whitespace")
	}
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse certificate %q: %w", id, err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, fmt.Errorf("parse certificate %q: certificate chain is empty", id)
	}
	documents := make([][]byte, len(certificate.Certificate))
	for index, der := range certificate.Certificate {
		parsed, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return nil, fmt.Errorf("parse certificate %q chain element %d: %w", id, index, parseErr)
		}
		if index == 0 {
			certificate.Leaf = parsed
		}
		documents[index] = append([]byte(nil), der...)
	}
	certificate.Certificate = documents
	return &Certificate{
		id:          id,
		certificate: certificate,
		fingerprint: fingerprint("gateway/certificate/v1", documents),
	}, nil
}

// ID returns the configuration-scoped material identifier.
func (c *Certificate) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// Fingerprint returns the public certificate-chain identity.
func (c *Certificate) Fingerprint() Fingerprint {
	if c == nil {
		return Fingerprint{}
	}
	return c.fingerprint
}

// TLSCertificate returns an independently mutable TLS value backed by the
// immutable parsed private key.
func (c *Certificate) TLSCertificate() tls.Certificate {
	if c == nil {
		return tls.Certificate{}
	}
	out := c.certificate
	out.Certificate = cloneByteSlices(c.certificate.Certificate)
	out.OCSPStaple = append([]byte(nil), c.certificate.OCSPStaple...)
	out.SignedCertificateTimestamps = cloneByteSlices(c.certificate.SignedCertificateTimestamps)
	return out
}

// NewTrustBundle parses one or more PEM CA certificates. Ordering and
// duplicates do not affect the returned fingerprint.
func NewTrustBundle(id string, caPEM []byte) (*TrustBundle, error) {
	if id == "" || strings.TrimSpace(id) != id {
		return nil, fmt.Errorf("trust bundle id must be non-empty without surrounding whitespace")
	}
	unique := make(map[string]*x509.Certificate)
	rest := caPEM
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			if len(bytes.TrimSpace(rest)) != 0 {
				return nil, fmt.Errorf("parse trust bundle %q: malformed PEM data", id)
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("parse trust bundle %q: PEM block %q is not a certificate", id, block.Type)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse trust bundle %q certificate: %w", id, err)
		}
		unique[string(certificate.Raw)] = certificate
		rest = remaining
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("parse trust bundle %q: CA set is empty", id)
	}

	documents := make([][]byte, 0, len(unique))
	for raw := range unique {
		documents = append(documents, []byte(raw))
	}
	sort.Slice(documents, func(left, right int) bool {
		return bytes.Compare(documents[left], documents[right]) < 0
	})
	certificates := make([]*x509.Certificate, len(documents))
	for index, document := range documents {
		certificates[index] = unique[string(document)]
	}
	return &TrustBundle{
		id:           id,
		certificates: certificates,
		fingerprint:  fingerprint("gateway/trust-bundle/v1", documents),
	}, nil
}

// ID returns the configuration-scoped trust-bundle identifier.
func (b *TrustBundle) ID() string {
	if b == nil {
		return ""
	}
	return b.id
}

// Fingerprint returns the canonical public CA-set identity.
func (b *TrustBundle) Fingerprint() Fingerprint {
	if b == nil {
		return Fingerprint{}
	}
	return b.fingerprint
}

// CertPool returns a new mutable pool containing the immutable CA set.
func (b *TrustBundle) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	if b == nil {
		return pool
	}
	for _, certificate := range b.certificates {
		pool.AddCert(certificate)
	}
	return pool
}

func fingerprint(domain string, documents [][]byte) Fingerprint {
	digest := sha256.New()
	writeLengthPrefixed(digest, []byte(domain))
	for _, document := range documents {
		writeLengthPrefixed(digest, document)
	}
	var out Fingerprint
	copy(out[:], digest.Sum(nil))
	return out
}

func writeLengthPrefixed(destination hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write(value)
}

func cloneByteSlices(values [][]byte) [][]byte {
	if values == nil {
		return nil
	}
	out := make([][]byte, len(values))
	for index := range values {
		out[index] = append([]byte(nil), values[index]...)
	}
	return out
}
