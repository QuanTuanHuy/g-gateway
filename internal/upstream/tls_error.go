package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
)

// TLSFailureClass is a bounded, non-sensitive category for an upstream TLS
// connection failure.
type TLSFailureClass string

const (
	// TLSFailureTrust identifies certificate-chain trust failures.
	TLSFailureTrust TLSFailureClass = "trust"
	// TLSFailureHostname identifies certificate hostname-verification failures.
	TLSFailureHostname TLSFailureClass = "hostname"
	// TLSFailureClientIdentity identifies a typed remote rejection of an mTLS
	// client identity.
	TLSFailureClientIdentity TLSFailureClass = "client_identity"
	// TLSFailureProtocol identifies strict application-protocol negotiation
	// failures.
	TLSFailureProtocol TLSFailureClass = "protocol"
	// TLSFailureHandshake identifies other typed TLS handshake failures.
	TLSFailureHandshake TLSFailureClass = "handshake"
)

var errHTTP2Required = errors.New("upstream requires negotiated HTTP/2")

// TLSFailureError retains an internal cause while exposing only a stable,
// redacted failure category through Error.
type TLSFailureError struct {
	Class TLSFailureClass
	Err   error
}

// Error returns a stable message that never includes details from the wrapped
// TLS or X.509 error.
func (e *TLSFailureError) Error() string {
	return fmt.Sprintf("upstream TLS failed: %s", e.Class)
}

// Unwrap returns the internal cause for typed control flow.
func (e *TLSFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsTLSFailure reports whether err contains a classified upstream TLS failure.
func IsTLSFailure(err error) bool {
	_, ok := TLSFailureClassOf(err)
	return ok
}

// TLSFailureClassOf returns the classified upstream TLS failure carried by
// err.
func TLSFailureClassOf(err error) (TLSFailureClass, bool) {
	var failure *TLSFailureError
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.Class, true
}

func classifyTLSFailure(err error, mtls bool) (*TLSFailureError, bool) {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return nil, false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return nil, false
	}
	if errors.Is(err, errHTTP2Required) {
		return &TLSFailureError{Class: TLSFailureProtocol, Err: err}, true
	}

	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return &TLSFailureError{Class: TLSFailureHostname, Err: err}, true
	}
	var unknownAuthorityError x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityError) {
		return &TLSFailureError{Class: TLSFailureTrust, Err: err}, true
	}
	var certificateInvalidError x509.CertificateInvalidError
	if errors.As(err, &certificateInvalidError) {
		return &TLSFailureError{Class: TLSFailureTrust, Err: err}, true
	}
	var verificationError *tls.CertificateVerificationError
	if errors.As(err, &verificationError) {
		return &TLSFailureError{Class: TLSFailureTrust, Err: err}, true
	}
	var alertError tls.AlertError
	if errors.As(err, &alertError) {
		class := TLSFailureHandshake
		if mtls && isClientIdentityAlert(alertError) {
			class = TLSFailureClientIdentity
		}
		return &TLSFailureError{Class: class, Err: err}, true
	}
	var recordHeaderError tls.RecordHeaderError
	if errors.As(err, &recordHeaderError) {
		return &TLSFailureError{Class: TLSFailureHandshake, Err: err}, true
	}
	return nil, false
}

func isClientIdentityAlert(alert tls.AlertError) bool {
	switch uint8(alert) {
	case 42, 43, 44, 45, 46, 48, 116:
		return true
	default:
		return false
	}
}
