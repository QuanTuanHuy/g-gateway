package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestPhase3C2ConcurrentHandshakeRotationReturnsToSteadyState(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	certificateFile, privateKeyFile := writeCertificatePair(t)
	first := gatewayTestCertificate(t, "default", 11, []string{"gateway.example"})
	second := gatewayTestCertificate(t, "default", 22, []string{"gateway.example"})
	resources := testResources(upstreamServer.URL)
	resources.Certificates = []*tlsmaterial.Certificate{first}
	resources.DownstreamTLS = &model.DownstreamTLSPolicy{DefaultCertificateRef: "default"}
	instance, err := New(testBootstrap(certificateFile, privateKeyFile), resources, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	httpsAddress := loopbackAddress(t, addresses.HTTPS)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	stop := make(chan struct{})
	errorsSeen := make(chan error, 1)
	ready := make(chan struct{}, 4)
	progress := make(chan struct{}, 1)
	var successfulHandshakes atomic.Uint64
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			dialer := &net.Dialer{Timeout: time.Second}
			reportedReady := false
			for {
				select {
				case <-stop:
					return
				default:
				}
				connection, dialErr := tls.DialWithDialer(dialer, "tcp", httpsAddress, &tls.Config{
					InsecureSkipVerify: true, // Generated test certificates rotate between private roots.
					ServerName:         "gateway.example",
					MinVersion:         tls.VersionTLS12,
				})
				if dialErr != nil {
					recordPhase3C2WorkerError(errorsSeen, dialErr)
					return
				}
				state := connection.ConnectionState()
				_ = connection.Close()
				if len(state.PeerCertificates) == 0 {
					recordPhase3C2WorkerError(errorsSeen, fmt.Errorf("TLS handshake returned no peer certificate"))
					return
				}
				serial := state.PeerCertificates[0].SerialNumber.Int64()
				if serial != 11 && serial != 22 {
					recordPhase3C2WorkerError(errorsSeen, fmt.Errorf("TLS handshake returned serial %d", serial))
					return
				}
				successfulHandshakes.Add(1)
				if !reportedReady {
					ready <- struct{}{}
					reportedReady = true
				}
				select {
				case progress <- struct{}{}:
				default:
				}
			}
		}()
	}
	var stopOnce sync.Once
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
		workers.Wait()
	}
	defer stopWorkers()
	for range 4 {
		select {
		case <-ready:
		case err := <-errorsSeen:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatal("TLS workers did not complete their initial handshakes")
		}
	}
	handshakeCheckpoint := successfulHandshakes.Load()

	for revision := uint64(2); revision <= 101; revision++ {
		rotated := model.CloneResourceSet(resources)
		rotated.Certificates[0] = second
		if revision%2 == 1 {
			rotated.Certificates[0] = first
		}
		if err := instance.Apply(revision, rotated); err != nil {
			t.Fatalf("Apply(%d) error = %v", revision, err)
		}
		if revision == 2 {
			handshakeCheckpoint = successfulHandshakes.Load()
		}
		if revision == 51 || revision == 101 {
			handshakeCheckpoint = waitForPhase3C2HandshakeProgress(
				t, errorsSeen, progress, &successfulHandshakes, handshakeCheckpoint,
			)
		}
	}
	stopWorkers()
	select {
	case err := <-errorsSeen:
		t.Fatal(err)
	default:
	}

	if got := instance.manager.Load().Revision(); got != 101 {
		t.Fatalf("active revision = %d, want 101", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && instance.manager.UpstreamStats().RetiredPlanSets != 0 {
		time.Sleep(time.Millisecond)
	}
	stats := instance.manager.UpstreamStats()
	if stats.RetiredPlanSets != 0 || stats.LiveTunnelLeases != 0 {
		t.Fatalf("registry did not reach steady state: %+v", stats)
	}
}

func waitForPhase3C2HandshakeProgress(
	t *testing.T,
	errorsSeen <-chan error,
	progress <-chan struct{},
	successfulHandshakes *atomic.Uint64,
	previous uint64,
) uint64 {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		if current := successfulHandshakes.Load(); current > previous {
			return current
		}
		select {
		case err := <-errorsSeen:
			t.Fatal(err)
		case <-progress:
		case <-timer.C:
			t.Fatalf("TLS handshakes did not progress beyond %d during rotation", previous)
		}
	}
}

func recordPhase3C2WorkerError(destination chan<- error, err error) {
	select {
	case destination <- err:
	default:
	}
}
