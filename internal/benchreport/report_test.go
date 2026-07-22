package benchreport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateParsesWrkPercentilesAndForcesDockerDesktopProvisional(t *testing.T) {
	input, output := fixtureDirs(t)
	writeComparison(t, input, "wrk", 2_000,
		[]runSpec{{rps: 900, p50: 50, p95: 80, p99: 100, rawP99: 999}},
		[]runSpec{{rps: 800, p50: 60, p95: 90, p99: 100}},
	)

	summary, err := Generate(Options{InputDir: input, OutputDir: output})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if summary.Verdict != VerdictProvisionalPass {
		t.Fatalf("verdict = %q, want %q", summary.Verdict, VerdictProvisionalPass)
	}
	if summary.EnvironmentClass != EnvironmentProvisional {
		t.Fatalf("environment class = %q, want forced provisional", summary.EnvironmentClass)
	}
	comparison := onlyComparison(t, summary)
	if comparison.Go.MedianP99US != 100 {
		t.Fatalf("Go p99 = %v, want structured wrk p99 100 (not raw-run p99)", comparison.Go.MedianP99US)
	}
	for _, name := range []string{"summary.json", "summary.csv", "summary.md"} {
		content, readErr := os.ReadFile(filepath.Join(output, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if !strings.Contains(string(content), string(VerdictProvisionalPass)) {
			t.Fatalf("%s does not contain verdict", name)
		}
	}
}

func TestGenerateCalculatesH2LoadNearestRankPercentiles(t *testing.T) {
	input, output := fixtureDirs(t)
	durations := []int64{4, 1, 100, 3, 2}
	writeComparison(t, input, "h2load", 2_000,
		[]runSpec{{rps: 900, requestDurations: durations}},
		[]runSpec{{rps: 800, requestDurations: durations}},
	)

	summary, err := Generate(Options{InputDir: input, OutputDir: output})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	got := onlyComparison(t, summary).Go
	if got.MedianP50US != 3 || got.MedianP95US != 100 || got.MedianP99US != 100 {
		t.Fatalf("h2load percentiles = (%v, %v, %v), want nearest-rank (3, 100, 100)", got.MedianP50US, got.MedianP95US, got.MedianP99US)
	}
}

func TestGenerateUsesMedianAcrossFiveRuns(t *testing.T) {
	input, output := fixtureDirs(t)
	writeComparison(t, input, "wrk", 1_000,
		[]runSpec{
			{rps: 100, p99: 50},
			{rps: 500, p99: 10},
			{rps: 300, p99: 30},
			{rps: 200, p99: 20},
			{rps: 400, p99: 40},
		},
		[]runSpec{
			{rps: 250, p99: 30}, {rps: 250, p99: 30}, {rps: 250, p99: 30},
			{rps: 250, p99: 30}, {rps: 250, p99: 30},
		},
	)

	summary, err := Generate(Options{InputDir: input, OutputDir: output})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	got := onlyComparison(t, summary).Go
	if got.RunCount != 5 || got.MedianRequestsPerSecond != 300 || got.MedianP99US != 30 {
		t.Fatalf("Go aggregate = %+v, want 5 runs, median RPS 300, median p99 30", got)
	}
}

func TestGenerateInvalidatesInsufficientDirectHeadroom(t *testing.T) {
	input, output := fixtureDirs(t)
	writeComparison(t, input, "wrk", 1_000,
		[]runSpec{{rps: 900, p99: 100}},
		[]runSpec{{rps: 850, p99: 100}},
	)

	summary, err := Generate(Options{InputDir: input, OutputDir: output})
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("Generate() error = %v, want ErrInvalidEvidence", err)
	}
	if summary.Verdict != VerdictInvalid || onlyComparison(t, summary).Verdict != VerdictInvalid {
		t.Fatalf("invalid headroom verdicts = overall %q comparison %q", summary.Verdict, onlyComparison(t, summary).Verdict)
	}
}

func TestGenerateInvalidatesGatewayErrors(t *testing.T) {
	input, output := fixtureDirs(t)
	writeComparison(t, input, "wrk", 2_000,
		[]runSpec{{rps: 900, p99: 100, non2xx: 1}},
		[]runSpec{{rps: 800, p99: 100}},
	)

	summary, err := Generate(Options{InputDir: input, OutputDir: output})
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("Generate() error = %v, want ErrInvalidEvidence", err)
	}
	if summary.Verdict != VerdictInvalid || onlyComparison(t, summary).Go.Non2xx != 1 {
		t.Fatalf("gateway error aggregate = %+v", summary)
	}
}

func TestGenerateClassifiesProvisionalMissWithoutError(t *testing.T) {
	tests := []struct {
		name   string
		goRun  runSpec
		apiRun runSpec
	}{
		{name: "throughput", goRun: runSpec{rps: 700, p99: 100}, apiRun: runSpec{rps: 800, p99: 100}},
		{name: "p99", goRun: runSpec{rps: 900, p99: 111}, apiRun: runSpec{rps: 800, p99: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, output := fixtureDirs(t)
			writeComparison(t, input, "wrk", 2_000, []runSpec{tt.goRun}, []runSpec{tt.apiRun})

			summary, err := Generate(Options{InputDir: input, OutputDir: output})
			if err != nil {
				t.Fatalf("Generate() error = %v; provisional miss must not fail the process", err)
			}
			if summary.Verdict != VerdictProvisionalMiss || onlyComparison(t, summary).Verdict != VerdictProvisionalMiss {
				t.Fatalf("verdict = %q, want provisional_miss", summary.Verdict)
			}
		})
	}
}

func TestGenerateRejectsMissingOrCorruptArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, input string)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, input string) {
				t.Helper()
				path := firstGoStructuredArtifact(t, input)
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove structured artifact: %v", err)
				}
			},
		},
		{
			name: "corrupt",
			mutate: func(t *testing.T, input string) {
				t.Helper()
				if err := os.WriteFile(firstGoStructuredArtifact(t, input), []byte("{"), 0o600); err != nil {
					t.Fatalf("corrupt structured artifact: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, output := fixtureDirs(t)
			writeComparison(t, input, "wrk", 2_000,
				[]runSpec{{rps: 900, p99: 100}},
				[]runSpec{{rps: 800, p99: 100}},
			)
			tt.mutate(t, input)
			if _, err := Generate(Options{InputDir: input, OutputDir: output}); err == nil {
				t.Fatal("Generate() error = nil, want artifact error")
			}
		})
	}
}

type runSpec struct {
	rps              float64
	p50              float64
	p95              float64
	p99              float64
	rawP99           float64
	requestDurations []int64
	requestErrors    int64
	timeouts         int64
	non2xx           int64
}

func fixtureDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "input"), filepath.Join(root, "output")
}

func writeComparison(t *testing.T, input, generator string, directRPS float64, goRuns, apiRuns []runSpec) {
	t.Helper()
	direct := runSpec{rps: directRPS, p50: 10, p95: 20, p99: 30, requestDurations: []int64{10, 20, 30}}
	writeRun(t, input, generator, "direct", 1, directRPS, direct)
	for i, spec := range goRuns {
		writeRun(t, input, generator, "go", i+1, directRPS, spec)
	}
	for i, spec := range apiRuns {
		writeRun(t, input, generator, "apisix", i+1, directRPS, spec)
	}
}

func writeRun(t *testing.T, input, generator, target string, repetition int, directRPS float64, spec runSpec) {
	t.Helper()
	relDir := filepath.Join(target, "scenario", "1024", fmt.Sprintf("run-%d", repetition))
	dir := filepath.Join(input, relDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	stdout := filepath.ToSlash(filepath.Join(relDir, "stdout.log"))
	stderr := filepath.ToSlash(filepath.Join(relDir, "stderr.log"))
	structured := filepath.ToSlash(filepath.Join(relDir, "generator.json"))
	requestLog := ""
	if generator == "h2load" {
		requestLog = filepath.ToSlash(filepath.Join(relDir, "requests.tsv"))
	}
	for _, path := range []string{stdout, stderr} {
		if err := os.WriteFile(filepath.Join(input, filepath.FromSlash(path)), []byte("fixture\n"), 0o600); err != nil {
			t.Fatalf("write fixture log: %v", err)
		}
	}

	requests := int64(10_000)
	if generator == "wrk" {
		structuredDoc := map[string]any{
			"schema_version": "wrk-1",
			"requests":       requests, "bytes": requests * 1024, "duration_us": 10_000_000,
			"requests_per_second": spec.rps, "transfer_bytes_per_second": spec.rps * 1024,
			"p50_us": spec.p50, "p95_us": spec.p95, "p99_us": spec.p99,
			"errors":  map[string]any{"connect": spec.requestErrors, "read": int64(0), "write": int64(0), "timeout": spec.timeouts},
			"non_2xx": spec.non2xx,
		}
		writeJSON(t, filepath.Join(input, filepath.FromSlash(structured)), structuredDoc)
	} else {
		durations := spec.requestDurations
		if len(durations) == 0 {
			durations = []int64{10, 20, 30}
		}
		requests = int64(len(durations))
		status4xx := spec.non2xx
		structuredDoc := map[string]any{
			"version":  "v1",
			"metadata": map[string]any{"generator": "h2load 1.69.0"},
			"measurements": map[string]any{
				"duration": 10.0, "request_per_second": spec.rps, "bytes_per_second": spec.rps * 1024,
				"requests":     map[string]any{"total": requests, "started": requests, "done": requests, "succeeded": requests - spec.requestErrors, "failed": spec.requestErrors, "errored": int64(0), "timeout": spec.timeouts},
				"status_codes": map[string]any{"2xx": requests - status4xx, "3xx": int64(0), "4xx": status4xx, "5xx": int64(0)},
			},
		}
		writeJSON(t, filepath.Join(input, filepath.FromSlash(structured)), structuredDoc)
		var lines strings.Builder
		for i, duration := range durations {
			status := 200
			if int64(i) < spec.non2xx {
				status = 500
			}
			fmt.Fprintf(&lines, "%d\t%d\t%d\n", i*100, status, duration)
		}
		if err := os.WriteFile(filepath.Join(input, filepath.FromSlash(requestLog)), []byte(lines.String()), 0o600); err != nil {
			t.Fatalf("write h2load request log: %v", err)
		}
	}

	rawP99 := spec.rawP99
	if rawP99 == 0 {
		rawP99 = spec.p99
	}
	if rawP99 == 0 && len(spec.requestDurations) > 0 {
		rawP99 = float64(spec.requestDurations[len(spec.requestDurations)-1])
	}
	version := "test-gateway"
	if target == "direct" {
		version = "nginx-1.31.3-alpine"
	}
	generatorVersion, generatorRevision := "wrk-4.2.0+monotonic-clock", "a211dd5a7050b1f9e8a9870b95513060e72ac4a0"
	protocol := "http/1.1"
	if generator == "h2load" {
		generatorVersion, generatorRevision = "h2load-1.69.0", "v1.69.0"
		protocol = "http/2"
	}
	raw := map[string]any{
		"schema_version": "1.0.0", "timestamp_utc": "2026-07-22T00:00:00Z", "environment_class": "dedicated_linux",
		"gateway_git_commit": strings.Repeat("a", 40),
		"target":             map[string]any{"name": target, "version": version, "apisix_commit": strings.Repeat("b", 40)},
		"image_ids":          map[string]any{"target": "sha256:target", "upstream": "sha256:upstream", "generator": "sha256:generator"},
		"scenario":           map[string]any{"name": "scenario", "generator": generator, "protocol": protocol, "tls": generator == "h2load"},
		"payload_bytes":      1024,
		"generator_settings": map[string]any{
			"generator_version": generatorVersion, "generator_revision": generatorRevision,
			"threads": 2, "connections": 16, "clients": 16, "streams_per_client": 16,
			"warmup_seconds": 3, "duration_seconds": 10, "repetition": repetition,
		},
		"target_limits":  map[string]any{"cpus": 2.0, "memory_bytes": int64(1_073_741_824), "workers": 2},
		"environment":    map[string]any{"docker_version": "27.0.3", "operating_system": "Docker Desktop (linux/x86_64)", "cpu": "20 logical CPUs"},
		"direct_control": map[string]any{"requests_per_second": directRPS, "required_headroom_factor": 1.25},
		"artifacts":      map[string]any{"stdout": stdout, "stderr": stderr, "structured": structured, "request_log": requestLog},
		"metrics":        map[string]any{"requests_per_second": spec.rps, "transfer_bytes_per_second": spec.rps * 1024, "p50_us": spec.p50, "p95_us": spec.p95, "p99_us": rawP99},
		"errors":         map[string]any{"request_errors": spec.requestErrors, "timeouts": spec.timeouts, "non_2xx": spec.non2xx},
	}
	writeJSON(t, filepath.Join(dir, "raw-run.json"), raw)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func onlyComparison(t *testing.T, summary Summary) Comparison {
	t.Helper()
	if len(summary.Comparisons) != 1 {
		t.Fatalf("comparison count = %d, want 1", len(summary.Comparisons))
	}
	return summary.Comparisons[0]
}

func firstGoStructuredArtifact(t *testing.T, input string) string {
	t.Helper()
	rawPath := filepath.Join(input, "go", "scenario", "1024", "run-1", "raw-run.json")
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw fixture: %v", err)
	}
	var raw struct {
		Artifacts struct {
			Structured string `json:"structured"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode raw fixture: %v", err)
	}
	return filepath.Join(input, filepath.FromSlash(raw.Artifacts.Structured))
}
