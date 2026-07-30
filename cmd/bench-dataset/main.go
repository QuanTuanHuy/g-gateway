// Package main implements the bench-dataset command, which generates
// deterministic equivalent G-Gateway and APISIX benchmark configuration
// artifacts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/QuanTuanHuy/g-gateway/internal/benchdataset"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("bench-dataset", flag.ContinueOnError)
	flags.SetOutput(stderr)
	routeCount := flags.Int("routes", 100_000, "number of routes to generate")
	seed := flags.Int64("seed", 20260723, "deterministic dataset seed")
	baselineSentinel := flags.String("baseline-sentinel", "", "one-route sentinel: first, middle, or last")
	gatewayOutput := flags.String("gateway-out", "", "gateway v1alpha2 YAML output path")
	apisixOutput := flags.String("apisix-out", "", "APISIX standalone YAML output path")
	metadataOutput := flags.String("metadata-out", "", "dataset metadata JSON output path")
	endpoint := flags.String("endpoint", "http://upstream-performance:8080", "fixed upstream endpoint")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected positional arguments: %v\n", flags.Args())
		return 2
	}
	for name, path := range map[string]string{
		"gateway-out":  *gatewayOutput,
		"apisix-out":   *apisixOutput,
		"metadata-out": *metadataOutput,
	} {
		if path == "" {
			_, _ = fmt.Fprintf(stderr, "-%s is required\n", name)
			return 2
		}
	}

	resources, metadata, err := benchdataset.Generate(benchdataset.Options{
		RouteCount:       *routeCount,
		Seed:             *seed,
		Endpoint:         *endpoint,
		BaselineSentinel: *baselineSentinel,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "generate dataset: %v\n", err)
		return 1
	}
	gatewayConfig, err := benchdataset.RenderGateway(
		resources,
		metadata,
		benchdataset.GatewayRenderOptions{},
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "render gateway config: %v\n", err)
		return 1
	}
	apisixConfig, err := benchdataset.RenderAPISIX(resources, metadata)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "render APISIX config: %v\n", err)
		return 1
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "encode metadata: %v\n", err)
		return 1
	}
	metadataJSON = append(metadataJSON, '\n')

	for _, artifact := range []struct {
		name string
		path string
		data []byte
	}{
		{name: "gateway config", path: *gatewayOutput, data: gatewayConfig},
		{name: "APISIX config", path: *apisixOutput, data: apisixConfig},
		{name: "metadata", path: *metadataOutput, data: metadataJSON},
	} {
		if err := writeArtifact(artifact.path, artifact.data); err != nil {
			_, _ = fmt.Fprintf(stderr, "write %s: %v\n", artifact.name, err)
			return 1
		}
	}
	return 0
}

func writeArtifact(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
