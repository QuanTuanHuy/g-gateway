package integration_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGatewayCommandReportsStructuredStartupFailure(t *testing.T) {
	binary := buildGatewayCommand(t)
	configFile := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(configFile, []byte("api_version: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(binary, "-config", configFile)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("gateway-dp exited successfully; output=%s", output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var event map[string]any
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &event) != nil {
		t.Fatalf("startup output is not structured JSON: %s", output)
	}
	if event["level"] != "ERROR" || event["msg"] != "gateway startup failed" || event["stage"] != "load_config" {
		t.Fatalf("startup event=%v", event)
	}
}

func TestGatewayCommandSIGTERMDrainsInFlightRequest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("canonical signal lifecycle test runs on Linux CI")
	}
	binary := buildGatewayCommand(t)

	started := make(chan struct{})
	release := make(chan struct{})
	upstream := http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/stream" {
			http.NotFound(response, request)
			return
		}
		_, _ = io.WriteString(response, "first\n")
		_ = http.NewResponseController(response).Flush()
		close(started)
		<-release
		_, _ = io.WriteString(response, "second\n")
	})}
	upstreamListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = upstream.Serve(upstreamListener) }()
	t.Cleanup(func() { _ = upstream.Close() })

	httpAddress := reserveTCPAddress(t)
	httpsAddress := reserveTCPAddress(t)
	adminAddress := reserveTCPAddress(t)
	certFile, keyFile := writeCertificatePair(t)
	configFile := filepath.Join(t.TempDir(), "gateway.yaml")
	document := processConfig(httpAddress, httpsAddress, adminAddress, certFile, keyFile, "http://"+upstreamListener.Addr().String())
	if err := os.WriteFile(configFile, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	command := exec.Command(binary, "-config", configFile)
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	processExited := false
	t.Cleanup(func() {
		if processExited {
			return
		}
		_ = command.Process.Kill()
		select {
		case <-exited:
		case <-time.After(time.Second):
		}
	})

	waitForHTTPStatus(t, "http://"+adminAddress+"/readyz", http.StatusOK)
	responseReady := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + httpAddress + "/stream")
		if requestErr != nil {
			requestError <- requestErr
			return
		}
		responseReady <- response
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("upstream request did not start; logs=%s", logs.String())
	}
	var response *http.Response
	select {
	case response = <-responseReady:
	case requestErr := <-requestError:
		t.Fatal(requestErr)
	case <-time.After(time.Second):
		t.Fatalf("first streamed response chunk was not forwarded; logs=%s", logs.String())
	}
	first, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || first != "first\n" {
		t.Fatalf("first response chunk=%q err=%v", first, err)
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitForHTTPStatus(t, "http://"+adminAddress+"/readyz", http.StatusServiceUnavailable)
	close(release)
	rest, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(rest) != "second\n" {
		t.Fatalf("drained response remainder=%q err=%v", rest, err)
	}
	select {
	case err := <-exited:
		processExited = true
		if err != nil {
			t.Fatalf("gateway-dp exit error=%v logs=%s", err, logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("gateway-dp did not exit after drain; logs=%s", logs.String())
	}
}

func buildGatewayCommand(t *testing.T) string {
	t.Helper()
	name := "gateway-dp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", binary, "./cmd/gateway-dp")
	command.Dir = repositoryRoot(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build gateway-dp: %v\n%s", err, output)
	}
	return binary
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForHTTPStatus(t *testing.T, target string, expected int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(target)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == expected {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not return status %d", target, expected)
}

func processConfig(httpAddress, httpsAddress, adminAddress, certFile, keyFile, upstream string) string {
	return fmt.Sprintf(`api_version: gateway/v1alpha1

listeners:
  http:
    address: %s
  https:
    address: %s
    certificate_file: '%s'
    private_key_file: '%s'
  admin:
    address: %s

server:
  read_header_timeout: 1s
  idle_timeout: 1m
  shutdown_timeout: 3s
  max_header_bytes: 1048576
  max_request_body_bytes: 1048576

telemetry:
  request_metrics_enabled: false
  profiling_enabled: false

routes:
  - id: process-test
    match:
      path: /stream
      methods: [GET]
    upstream_ref: process-test

upstreams:
  - id: process-test
    endpoints: [%s]
    transport:
      dial_timeout: 1s
      response_header_timeout: 3s
      idle_connection_timeout: 1m
      max_idle_connections: 4
      max_idle_connections_per_host: 4
`, httpAddress, httpsAddress, strings.ReplaceAll(certFile, "'", "''"), strings.ReplaceAll(keyFile, "'", "''"), adminAddress, upstream)
}
