package proxy_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGRPCTLSUnaryPassThrough(t *testing.T) {
	identity := newGRPCTestIdentity(t)
	service := newGRPCInteropService()
	upstreamAddress := startGRPCTestServer(t, identity, service, true)
	client := startGRPCTestGateway(t, identity, "https://"+upstreamAddress)

	ctx := metadata.NewOutgoingContext(
		context.Background(),
		metadata.Pairs("x-client-metadata", "preserve-me"),
	)
	var header metadata.MD
	var trailer metadata.MD
	response, err := client.UnaryCall(
		ctx,
		&grpc_testing.SimpleRequest{
			Payload: &grpc_testing.Payload{Body: []byte("unary-payload")},
		},
		grpc.Header(&header),
		grpc.Trailer(&trailer),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(response.GetPayload().GetBody()); got != "unary-payload" {
		t.Fatalf("unary payload=%q", got)
	}
	if got := header.Get("x-upstream-header"); len(got) != 1 || got[0] != "present" {
		t.Fatalf("header=%v", header)
	}
	if got := header.Get("x-seen-client"); len(got) != 1 || got[0] != "preserve-me" {
		t.Fatalf("forwarded metadata=%v", header)
	}
	if got := trailer.Get("x-upstream-trailer"); len(got) != 1 || got[0] != "complete" {
		t.Fatalf("trailer=%v", trailer)
	}

	_, err = client.UnaryCall(
		context.Background(),
		&grpc_testing.SimpleRequest{
			Payload: &grpc_testing.Payload{Body: []byte("deny")},
		},
	)
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "denied" {
		t.Fatalf("status=%v, want PermissionDenied denied", err)
	}
}

func TestGRPCStreamingPassThrough(t *testing.T) {
	identity := newGRPCTestIdentity(t)
	service := newGRPCInteropService()
	upstreamAddress := startGRPCTestServer(t, identity, service, true)
	client := startGRPCTestGateway(t, identity, "https://"+upstreamAddress)

	input, err := client.StreamingInputCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{[]byte("abc"), []byte("12345")} {
		if err := input.Send(&grpc_testing.StreamingInputCallRequest{
			Payload: &grpc_testing.Payload{Body: body},
		}); err != nil {
			t.Fatal(err)
		}
	}
	inputResponse, err := input.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	if inputResponse.GetAggregatedPayloadSize() != 8 {
		t.Fatalf("aggregated payload=%d", inputResponse.GetAggregatedPayloadSize())
	}

	output, err := client.StreamingOutputCall(
		context.Background(),
		&grpc_testing.StreamingOutputCallRequest{
			ResponseParameters: []*grpc_testing.ResponseParameters{
				{Size: 3},
				{Size: 5},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, wantSize := range []int{3, 5} {
		message, recvErr := output.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wantBody := bytes.Repeat([]byte{byte(index + 1)}, wantSize)
		if !bytes.Equal(message.GetPayload().GetBody(), wantBody) {
			t.Fatalf("server stream message %d=%v, want %v", index, message.GetPayload().GetBody(), wantBody)
		}
	}
	if _, err := output.Recv(); err != io.EOF {
		t.Fatalf("server stream terminal error=%v, want EOF", err)
	}

	assertGRPCBidiEcho(t, client, "tls-before", "tls-after")
}

func TestGRPCH2CPassThrough(t *testing.T) {
	identity := newGRPCTestIdentity(t)
	service := newGRPCInteropService()
	upstreamAddress := startGRPCTestServer(t, identity, service, false)
	client := startGRPCTestGateway(t, identity, "http://"+upstreamAddress)

	response, err := client.UnaryCall(
		context.Background(),
		&grpc_testing.SimpleRequest{
			Payload: &grpc_testing.Payload{Body: []byte("h2c-unary")},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(response.GetPayload().GetBody()); got != "h2c-unary" {
		t.Fatalf("h2c unary payload=%q", got)
	}
	assertGRPCBidiEcho(t, client, "h2c-before", "h2c-after")
}

func TestGRPCCancellationPassThrough(t *testing.T) {
	identity := newGRPCTestIdentity(t)
	service := newGRPCInteropService()
	upstreamAddress := startGRPCTestServer(t, identity, service, true)
	client := startGRPCTestGateway(t, identity, "https://"+upstreamAddress)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.FullDuplexCall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&grpc_testing.StreamingOutputCallRequest{
		Payload: &grpc_testing.Payload{Body: []byte("first")},
	}); err != nil {
		t.Fatal(err)
	}
	response, err := stream.Recv()
	if err != nil || string(response.GetPayload().GetBody()) != "first" {
		t.Fatalf("first response=%v err=%v", response, err)
	}
	cancel()
	if _, err := stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled stream error=%v", err)
	}
	select {
	case <-service.canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream gRPC handler did not observe cancellation")
	}
	if calls := service.bidiCalls.Load(); calls != 1 {
		t.Fatalf("bidi upstream calls=%d, want one attempt", calls)
	}
}

func assertGRPCBidiEcho(
	t *testing.T,
	client grpc_testing.TestServiceClient,
	messages ...string,
) {
	t.Helper()
	stream, err := client.FullDuplexCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if err := stream.Send(&grpc_testing.StreamingOutputCallRequest{
			Payload: &grpc_testing.Payload{Body: []byte(message)},
		}); err != nil {
			t.Fatal(err)
		}
		response, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if got := string(response.GetPayload().GetBody()); got != message {
			t.Fatalf("bidi response=%q, want %q", got, message)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("bidi terminal error=%v, want EOF", err)
	}
}

type grpcInteropService struct {
	grpc_testing.UnimplementedTestServiceServer
	canceled   chan struct{}
	cancelOnce sync.Once
	bidiCalls  atomic.Int32
}

func newGRPCInteropService() *grpcInteropService {
	return &grpcInteropService{canceled: make(chan struct{})}
}

func (s *grpcInteropService) UnaryCall(
	ctx context.Context,
	request *grpc_testing.SimpleRequest,
) (*grpc_testing.SimpleResponse, error) {
	incoming, _ := metadata.FromIncomingContext(ctx)
	header := metadata.Pairs("x-upstream-header", "present")
	if values := incoming.Get("x-client-metadata"); len(values) > 0 {
		header.Append("x-seen-client", values[0])
	}
	if err := grpc.SetHeader(ctx, header); err != nil {
		return nil, err
	}
	grpc.SetTrailer(ctx, metadata.Pairs("x-upstream-trailer", "complete"))
	if string(request.GetPayload().GetBody()) == "deny" {
		return nil, status.Error(codes.PermissionDenied, "denied")
	}
	return &grpc_testing.SimpleResponse{
		Payload: &grpc_testing.Payload{
			Body: append([]byte(nil), request.GetPayload().GetBody()...),
		},
	}, nil
}

func (s *grpcInteropService) StreamingInputCall(
	stream grpc.ClientStreamingServer[
		grpc_testing.StreamingInputCallRequest,
		grpc_testing.StreamingInputCallResponse,
	],
) error {
	var total int32
	for {
		request, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&grpc_testing.StreamingInputCallResponse{
				AggregatedPayloadSize: total,
			})
		}
		if err != nil {
			return err
		}
		total += int32(len(request.GetPayload().GetBody()))
	}
}

func (s *grpcInteropService) StreamingOutputCall(
	request *grpc_testing.StreamingOutputCallRequest,
	stream grpc.ServerStreamingServer[grpc_testing.StreamingOutputCallResponse],
) error {
	for index, parameter := range request.GetResponseParameters() {
		if err := stream.Send(&grpc_testing.StreamingOutputCallResponse{
			Payload: &grpc_testing.Payload{
				Body: bytes.Repeat([]byte{byte(index + 1)}, int(parameter.GetSize())),
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *grpcInteropService) FullDuplexCall(
	stream grpc.BidiStreamingServer[
		grpc_testing.StreamingOutputCallRequest,
		grpc_testing.StreamingOutputCallResponse,
	],
) error {
	s.bidiCalls.Add(1)
	for {
		request, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if stream.Context().Err() != nil {
				s.cancelOnce.Do(func() { close(s.canceled) })
			}
			return err
		}
		if err := stream.Send(&grpc_testing.StreamingOutputCallResponse{
			Payload: &grpc_testing.Payload{
				Body: append([]byte(nil), request.GetPayload().GetBody()...),
			},
		}); err != nil {
			return err
		}
	}
}

type grpcTestIdentity struct {
	certificate    tls.Certificate
	certificatePEM []byte
	privateKeyPEM  []byte
}

func newGRPCTestIdentity(t *testing.T) grpcTestIdentity {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "grpc.test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		publicKey,
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificateDER,
	})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyDER,
	})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return grpcTestIdentity{
		certificate:    certificate,
		certificatePEM: certificatePEM,
		privateKeyPEM:  privateKeyPEM,
	}
}

func startGRPCTestServer(
	t *testing.T,
	identity grpcTestIdentity,
	service *grpcInteropService,
	useTLS bool,
) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if useTLS {
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{identity.certificate},
		})))
	}
	server := grpc.NewServer(options...)
	grpc_testing.RegisterTestServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func startGRPCTestGateway(
	t *testing.T,
	identity grpcTestIdentity,
	upstreamURL string,
) grpc_testing.TestServiceClient {
	t.Helper()
	noTotalTimeout := time.Duration(0)
	directory := t.TempDir()
	certificateFile := filepath.Join(directory, "gateway.crt")
	privateKeyFile := filepath.Join(directory, "gateway.key")
	if err := os.WriteFile(certificateFile, identity.certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyFile, identity.privateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap := config.BootstrapConfig{
		HTTP: config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{
			Address:         "127.0.0.1:0",
			CertificateFile: certificateFile,
			PrivateKeyFile:  privateKeyFile,
		},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     3 * time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
	resources := model.ResourceSet{
		Routes: []model.Route{{
			ID: "grpc",
			Match: model.RouteMatch{
				Path:    "/grpc.testing.TestService/*",
				Methods: []string{http.MethodPost},
			},
			UpstreamRef: "grpc",
			Resilience: model.RouteResiliencePolicy{
				TotalTimeout: &noTotalTimeout,
			},
		}},
		Upstreams: []model.Upstream{{
			ID:        "grpc",
			Endpoints: []model.Endpoint{{URL: upstreamURL, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol:                  model.TransportProtocolHTTP2,
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        8,
				MaxIdleConnectionsPerHost: 8,
			},
		}},
	}
	if len(upstreamURL) >= len("https://") && upstreamURL[:len("https://")] == "https://" {
		trustBundle, err := tlsmaterial.NewTrustBundle("grpc-root", identity.certificatePEM)
		if err != nil {
			t.Fatal(err)
		}
		resources.Upstreams[0].Transport.TLS = &model.UpstreamTLSPolicy{
			TrustBundleRef: "grpc-root",
		}
		resources.TrustBundles = []*tlsmaterial.TrustBundle{trustBundle}
	}
	instance, err := gateway.New(
		bootstrap,
		resources,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("gateway shutdown: %v", err)
		}
	})

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(identity.certificatePEM) {
		t.Fatal("append downstream root")
	}
	connection, err := grpc.NewClient(
		loopbackGRPCAddress(t, addresses.HTTPS),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: "127.0.0.1",
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
	})
	return grpc_testing.NewTestServiceClient(connection)
}

func loopbackGRPCAddress(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort("127.0.0.1", port)
}
