package upstream

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/interop/grpc_testing"
)

func BenchmarkTransportProfile(b *testing.B) {
	pki := newUpstreamTestPKI(b, "profile-benchmark-root")
	trust := mustTrustBundle(b, "root", pki.rootPEM)
	materials := materialIndex{trustBundles: map[string]*tlsmaterial.TrustBundle{"root": trust}}
	tests := []struct {
		name     string
		endpoint string
		protocol model.TransportProtocol
		tls      *model.UpstreamTLSPolicy
	}{
		{name: "cleartext-http1", endpoint: "http://upstream.example:80", protocol: model.TransportProtocolHTTP1},
		{name: "https-http1", endpoint: "https://upstream.example:443", protocol: model.TransportProtocolHTTP1, tls: &model.UpstreamTLSPolicy{TrustBundleRef: "root", ServerName: "upstream.example"}},
		{name: "https-http2", endpoint: "https://upstream.example:443", protocol: model.TransportProtocolHTTP2, tls: &model.UpstreamTLSPolicy{TrustBundleRef: "root", ServerName: "upstream.example"}},
		{name: "h2c", endpoint: "http://upstream.example:80", protocol: model.TransportProtocolHTTP2},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			resource := phase3C1BenchmarkUpstream("benchmark", test.endpoint, test.protocol, test.tls)
			b.ReportAllocs()
			for b.Loop() {
				profile, err := compileTransportProfile(resource, materials)
				if err != nil {
					b.Fatal(err)
				}
				_ = makeTransportKey(profile)
			}
		})
	}
}

func BenchmarkTLSHandshake(b *testing.B) {
	server, profile := phase3C1BenchmarkTLSServer(b, model.TransportProtocolHTTP1, false)
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			runtime := newTransportRuntime(profile, nil)
			phase3C1BenchmarkRoundTrip(b, runtime, server)
			runtime.CloseIdleConnections()
		}
	})
	b.Run("resumed", func(b *testing.B) {
		runtime := newTransportRuntime(profile, nil)
		defer runtime.CloseIdleConnections()
		phase3C1BenchmarkRoundTrip(b, runtime, server)
		runtime.CloseIdleConnections()
		b.ResetTimer()
		b.ReportAllocs()
		for b.Loop() {
			phase3C1BenchmarkRoundTrip(b, runtime, server)
			runtime.CloseIdleConnections()
		}
	})
}

func BenchmarkHTTPProtocol(b *testing.B) {
	httpsH1, httpsH1Profile := phase3C1BenchmarkTLSServer(b, model.TransportProtocolHTTP1, false)
	httpsH2, httpsH2Profile := phase3C1BenchmarkTLSServer(b, model.TransportProtocolHTTP2, true)
	h2c := startH2CServer(b, phase3C1BenchmarkHandler())
	h2cProfile := integrationTransportProfile(h2c, model.TransportProtocolHTTP2, nil, nil, "")
	tests := []struct {
		name       string
		rawURL     string
		profile    transportProfile
		concurrent bool
	}{
		{name: "https-http1", rawURL: httpsH1, profile: httpsH1Profile},
		{name: "https-http2-multiplexed", rawURL: httpsH2, profile: httpsH2Profile, concurrent: true},
		{name: "h2c", rawURL: h2c, profile: h2cProfile},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			runtime := newTransportRuntime(test.profile, nil)
			defer runtime.CloseIdleConnections()
			phase3C1BenchmarkRoundTrip(b, runtime, test.rawURL)
			b.ResetTimer()
			b.ReportAllocs()
			if test.concurrent {
				b.RunParallel(func(parallel *testing.PB) {
					for parallel.Next() {
						phase3C1BenchmarkRoundTrip(b, runtime, test.rawURL)
					}
				})
				return
			}
			for b.Loop() {
				phase3C1BenchmarkRoundTrip(b, runtime, test.rawURL)
			}
		})
	}
}

func BenchmarkGRPC(b *testing.B) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	server := grpc.NewServer()
	grpc_testing.RegisterTestServiceServer(server, phase3C1BenchmarkGRPCService{})
	go func() {
		_ = server.Serve(listener)
	}()
	b.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = connection.Close() })
	client := grpc_testing.NewTestServiceClient(connection)
	unaryRequest := &grpc_testing.SimpleRequest{
		Payload: &grpc_testing.Payload{Body: bytes.Repeat([]byte("x"), 256)},
	}
	if _, err := client.UnaryCall(context.Background(), unaryRequest); err != nil {
		b.Fatal(err)
	}

	b.Run("unary", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := client.UnaryCall(context.Background(), unaryRequest); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("server-streaming", func(b *testing.B) {
		request := &grpc_testing.StreamingOutputCallRequest{
			ResponseParameters: []*grpc_testing.ResponseParameters{{Size: 128}, {Size: 128}},
		}
		b.ReportAllocs()
		for b.Loop() {
			stream, streamErr := client.StreamingOutputCall(context.Background(), request)
			if streamErr != nil {
				b.Fatal(streamErr)
			}
			for {
				if _, recvErr := stream.Recv(); recvErr != nil {
					if recvErr != io.EOF {
						b.Fatal(recvErr)
					}
					break
				}
			}
		}
	})
}

func BenchmarkTransportGeneration(b *testing.B) {
	pkiA := newUpstreamTestPKI(b, "generation-a")
	pkiB := newUpstreamTestPKI(b, "generation-b")
	trustA := mustTrustBundle(b, "root", pkiA.rootPEM)
	trustB := mustTrustBundle(b, "root", pkiB.rootPEM)
	resource := phase3C1BenchmarkUpstream(
		"benchmark",
		"https://upstream.example:443",
		model.TransportProtocolHTTP2,
		&model.UpstreamTLSPolicy{TrustBundleRef: "root", ServerName: "upstream.example"},
	)
	profile := integrationTransportProfile(
		"https://upstream.example:443",
		model.TransportProtocolHTTP2,
		trustA,
		nil,
		"upstream.example",
	)
	b.Run("create", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			runtime := newTransportRuntime(profile, nil)
			runtime.CloseIdleConnections()
		}
	})
	for _, test := range []struct {
		name    string
		bundles []*tlsmaterial.TrustBundle
	}{
		{name: "reuse", bundles: []*tlsmaterial.TrustBundle{trustA}},
		{name: "rotate", bundles: []*tlsmaterial.TrustBundle{trustB}},
	} {
		b.Run(test.name, func(b *testing.B) {
			registry, registryErr := NewRegistry(RegistryOptions{
				MaxRetiredSnapshots: 4,
				HealthWorkers:       1,
				HealthQueueCapacity: 1,
			})
			if registryErr != nil {
				b.Fatal(registryErr)
			}
			initial, prepareErr := registry.Prepare(model.ResourceSet{
				Upstreams:    []model.Upstream{resource},
				TrustBundles: []*tlsmaterial.TrustBundle{trustA},
			})
			if prepareErr != nil {
				b.Fatal(prepareErr)
			}
			active := initial.Commit()
			b.Cleanup(func() {
				active.Retire()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if closeErr := registry.Close(ctx); closeErr != nil {
					b.Errorf("close registry: %v", closeErr)
				}
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				candidate, candidateErr := registry.Prepare(model.ResourceSet{
					Upstreams:    []model.Upstream{resource},
					TrustBundles: test.bundles,
				})
				if candidateErr != nil {
					b.Fatal(candidateErr)
				}
				candidate.Rollback()
			}
		})
	}
}

type phase3C1BenchmarkGRPCService struct {
	grpc_testing.UnimplementedTestServiceServer
}

func (phase3C1BenchmarkGRPCService) UnaryCall(
	_ context.Context,
	request *grpc_testing.SimpleRequest,
) (*grpc_testing.SimpleResponse, error) {
	return &grpc_testing.SimpleResponse{
		Payload: &grpc_testing.Payload{Body: request.GetPayload().GetBody()},
	}, nil
}

func (phase3C1BenchmarkGRPCService) StreamingOutputCall(
	request *grpc_testing.StreamingOutputCallRequest,
	stream grpc.ServerStreamingServer[grpc_testing.StreamingOutputCallResponse],
) error {
	for _, parameter := range request.GetResponseParameters() {
		if err := stream.Send(&grpc_testing.StreamingOutputCallResponse{
			Payload: &grpc_testing.Payload{Body: make([]byte, parameter.GetSize())},
		}); err != nil {
			return err
		}
	}
	return nil
}

func phase3C1BenchmarkTLSServer(
	b testing.TB,
	protocol model.TransportProtocol,
	http2 bool,
) (string, transportProfile) {
	b.Helper()
	pki := newUpstreamTestPKI(b, "benchmark-root")
	certificatePEM, privateKeyPEM := pki.issue(b, certificateRequest{
		commonName: "upstream.internal",
		dnsNames:   []string{"upstream.internal"},
		usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	nextProtocols := []string{"http/1.1"}
	if http2 {
		nextProtocols = []string{"h2"}
	}
	server := startUpstreamTLSServer(b, upstreamTLSServerOptions{
		certificatePEM: certificatePEM,
		privateKeyPEM:  privateKeyPEM,
		nextProtocols:  nextProtocols,
		http2:          http2,
	}, phase3C1BenchmarkHandler())
	return server, integrationTransportProfile(
		server,
		protocol,
		mustTrustBundle(b, "root", pki.rootPEM),
		nil,
		"upstream.internal",
	)
}

func phase3C1BenchmarkHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
}

func phase3C1BenchmarkRoundTrip(
	b testing.TB,
	runtime *transportRuntime,
	rawURL string,
) {
	b.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		b.Fatal(err)
	}
	response, err := runtime.RoundTrip(request)
	if err != nil {
		b.Fatal(err)
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		b.Fatal(closeErr)
	}
}

func phase3C1BenchmarkUpstream(
	id, endpoint string,
	protocol model.TransportProtocol,
	tlsPolicy *model.UpstreamTLSPolicy,
) model.Upstream {
	transport := validTransportConfig()
	transport.Protocol = protocol
	transport.TLS = tlsPolicy
	return model.Upstream{
		ID:        id,
		Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
		Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
		Transport: transport,
	}
}
