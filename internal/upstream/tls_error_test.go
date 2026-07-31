package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestTLSFailureClassificationUsesTypedErrors(t *testing.T) {
	certificate := &x509.Certificate{}
	tests := []struct {
		name  string
		err   error
		mtls  bool
		class TLSFailureClass
		ok    bool
	}{
		{
			name:  "unknown authority",
			err:   x509.UnknownAuthorityError{Cert: certificate},
			class: TLSFailureTrust,
			ok:    true,
		},
		{
			name:  "invalid certificate",
			err:   x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired},
			class: TLSFailureTrust,
			ok:    true,
		},
		{
			name:  "hostname",
			err:   x509.HostnameError{Certificate: certificate, Host: "secret.internal"},
			class: TLSFailureHostname,
			ok:    true,
		},
		{
			name: "verification wrapper",
			err: &tls.CertificateVerificationError{
				Err: x509.UnknownAuthorityError{Cert: certificate},
			},
			class: TLSFailureTrust,
			ok:    true,
		},
		{
			name:  "record header",
			err:   tls.RecordHeaderError{},
			class: TLSFailureHandshake,
			ok:    true,
		},
		{
			name:  "strict protocol",
			err:   errHTTP2Required,
			class: TLSFailureProtocol,
			ok:    true,
		},
		{
			name: "deadline",
			err:  context.DeadlineExceeded,
		},
		{
			name: "cancellation",
			err:  context.Canceled,
		},
		{
			name: "network",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure, ok := classifyTLSFailure(test.err, test.mtls)
			if ok != test.ok {
				t.Fatalf("classified=%v, want %v (%v)", ok, test.ok, failure)
			}
			if ok && failure.Class != test.class {
				t.Fatalf("class=%q, want %q", failure.Class, test.class)
			}
		})
	}
}

func TestTLSFailureClassifiesClientIdentityAlertsOnlyForMTLS(t *testing.T) {
	clientIdentityAlerts := []tls.AlertError{42, 43, 44, 45, 46, 48, 116}
	for _, alert := range clientIdentityAlerts {
		failure, ok := classifyTLSFailure(alert, true)
		if !ok || failure.Class != TLSFailureClientIdentity {
			t.Fatalf("mTLS alert %d = %+v, %v", alert, failure, ok)
		}
		failure, ok = classifyTLSFailure(alert, false)
		if !ok || failure.Class != TLSFailureHandshake {
			t.Fatalf("server-auth alert %d = %+v, %v", alert, failure, ok)
		}
	}

	failure, ok := classifyTLSFailure(tls.AlertError(40), true)
	if !ok || failure.Class != TLSFailureHandshake {
		t.Fatalf("generic mTLS alert = %+v, %v", failure, ok)
	}
}

func TestTLSFailureErrorRedactsWrappedDetails(t *testing.T) {
	cause := errors.New("certificate secret.internal signed by private-ca")
	failure := &TLSFailureError{Class: TLSFailureTrust, Err: cause}
	if got := failure.Error(); got != "upstream TLS failed: trust" {
		t.Fatalf("Error()=%q", got)
	}
	if !errors.Is(failure, cause) {
		t.Fatal("TLS failure does not unwrap its internal cause")
	}
	if !IsTLSFailure(failure) {
		t.Fatal("IsTLSFailure=false")
	}
	class, ok := TLSFailureClassOf(failure)
	if !ok || class != TLSFailureTrust {
		t.Fatalf("TLSFailureClassOf=(%q,%v)", class, ok)
	}
}

type recordingTLSObserver struct {
	handshakes []tlsHandshakeObservation
	failures   []TLSFailureClass
}

type tlsHandshakeObservation struct {
	result   string
	mode     string
	protocol model.TransportProtocol
}

func (o *recordingTLSObserver) ObserveTLSHandshake(
	result, mode string,
	protocol model.TransportProtocol,
) {
	o.handshakes = append(o.handshakes, tlsHandshakeObservation{
		result: result, mode: mode, protocol: protocol,
	})
}

func (o *recordingTLSObserver) ObserveTLSFailure(class TLSFailureClass) {
	o.failures = append(o.failures, class)
}
