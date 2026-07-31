package tlsmaterial

import (
	"fmt"
	"io"
	"os"
)

const (
	// MaxCAFileBytes is the maximum accepted CA bundle file size.
	MaxCAFileBytes int64 = 1 << 20
	// MaxCertificateFileBytes is the maximum accepted certificate-chain file
	// size.
	MaxCertificateFileBytes int64 = 256 << 10
	// MaxPrivateKeyFileBytes is the maximum accepted private-key file size.
	MaxPrivateKeyFileBytes int64 = 256 << 10
	// MaxMaterialResources bounds certificates and trust bundles in one
	// candidate resource set.
	MaxMaterialResources = 10_000
	// MaxCandidateSourceBytes bounds aggregate TLS source bytes in one
	// candidate resource set.
	MaxCandidateSourceBytes int64 = 64 << 20
)

// LoadCertificate reads bounded certificate and private-key files and returns
// immutable parsed material plus their combined source byte count.
func LoadCertificate(id, certificatePath, privateKeyPath string) (*Certificate, int64, error) {
	certificatePEM, err := readBoundedFile("certificate_file", certificatePath, MaxCertificateFileBytes)
	if err != nil {
		return nil, 0, err
	}
	privateKeyPEM, err := readBoundedFile("private_key_file", privateKeyPath, MaxPrivateKeyFileBytes)
	if err != nil {
		return nil, 0, err
	}
	certificate, err := NewCertificate(id, certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, 0, err
	}
	return certificate, int64(len(certificatePEM) + len(privateKeyPEM)), nil
}

// LoadTrustBundle reads one bounded CA file and returns immutable parsed
// material plus its source byte count.
func LoadTrustBundle(id, path string) (*TrustBundle, int64, error) {
	caPEM, err := readBoundedFile("ca_file", path, MaxCAFileBytes)
	if err != nil {
		return nil, 0, err
	}
	bundle, err := NewTrustBundle(id, caPEM)
	if err != nil {
		return nil, 0, err
	}
	return bundle, int64(len(caPEM)), nil
}

func readBoundedFile(field, path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s: path must not be empty", field)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open: %w", field, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: stat: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: path is not a regular file", field)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s: file size %d exceeds %d bytes", field, info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", field, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: source exceeds %d bytes", field, limit)
	}
	return data, nil
}
