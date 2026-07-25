package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/benchdataset"
)

func TestRunWritesGatewayAPISIXAndMetadataArtifacts(t *testing.T) {
	directory := t.TempDir()
	gatewayPath := filepath.Join(directory, "gateway.yaml")
	apisixPath := filepath.Join(directory, "apisix.yaml")
	metadataPath := filepath.Join(directory, "metadata.json")
	var stderr strings.Builder

	exitCode := run([]string{
		"-routes", "100",
		"-seed", "20260723",
		"-gateway-out", gatewayPath,
		"-apisix-out", apisixPath,
		"-metadata-out", metadataPath,
		"-endpoint", "http://upstream-performance:8080",
	}, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit = %d, stderr=%q", exitCode, stderr.String())
	}

	gatewayConfig, err := os.ReadFile(gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	apisixConfig, err := os.ReadFile(apisixPath)
	if err != nil {
		t.Fatal(err)
	}
	metadataJSON, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata benchdataset.Metadata
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.RouteCount != 100 || metadata.Checksum == "" {
		t.Fatalf("metadata = %+v", metadata)
	}
	for name, content := range map[string][]byte{
		"gateway": gatewayConfig,
		"apisix":  apisixConfig,
	} {
		if !strings.Contains(string(content), "# benchmark_checksum: "+metadata.Checksum) {
			t.Fatalf("%s artifact does not contain metadata checksum", name)
		}
	}
}
