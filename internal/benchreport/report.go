// Package benchreport turns black-box benchmark artifacts into deterministic
// comparison summaries. It deliberately has no dependency on gateway runtime
// packages.
package benchreport

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Verdict classifies the aggregate or one scenario/payload comparison.
type Verdict string

const (
	// VerdictInvalid means evidence is incomplete, erroneous, or lacks required
	// direct-control headroom.
	VerdictInvalid Verdict = "invalid"
	// VerdictProvisionalPass means Go meets the phase-1 throughput, p99, and
	// error-rate parity thresholds.
	VerdictProvisionalPass Verdict = "provisional_pass"
	// VerdictProvisionalMiss means valid evidence misses at least one phase-1
	// parity threshold.
	VerdictProvisionalMiss Verdict = "provisional_miss"

	// EnvironmentProvisional identifies any run set containing provisional
	// metadata or Docker Desktop evidence.
	EnvironmentProvisional = "provisional"
	// EnvironmentDedicatedLinux identifies a run set whose every artifact is
	// marked dedicated Linux and is not from Docker Desktop. Verdict names
	// remain provisional in this phase-1 report schema.
	EnvironmentDedicatedLinux = "dedicated_linux"
)

// ErrInvalidEvidence is returned after reports are written when an aggregated
// comparison has invalid measured evidence. Callers should use errors.Is.
var ErrInvalidEvidence = errors.New("invalid benchmark evidence")

// Options identifies the evidence tree to read and the report directory to
// create or update.
type Options struct {
	// InputDir is an existing directory recursively searched for raw-run.json
	// artifacts and their relative evidence files.
	InputDir string
	// OutputDir receives summary.json, summary.csv, and summary.md.
	OutputDir string
}

// Summary is the deterministic report over all scenario/payload comparisons.
type Summary struct {
	// SchemaVersion is the report schema version.
	SchemaVersion string `json:"schema_version"`
	// EnvironmentClass is provisional if any input is provisional or reports
	// Docker Desktop; otherwise it is dedicated_linux.
	EnvironmentClass string `json:"environment_class"`
	// Verdict is invalid if any comparison is invalid, then provisional_miss
	// if any valid comparison misses, and provisional_pass otherwise.
	Verdict Verdict `json:"verdict"`
	// Comparisons is sorted by scenario and then payload size.
	Comparisons []Comparison `json:"comparisons"`
}

// Comparison summarizes one scenario and payload across direct, Go, and
// APISIX runs.
type Comparison struct {
	// Scenario is the benchmark scenario name.
	Scenario string `json:"scenario"`
	// PayloadBytes is the response payload size in bytes.
	PayloadBytes int64 `json:"payload_bytes"`
	// Generator is "wrk" or "h2load".
	Generator string `json:"generator"`
	// Protocol is the declared downstream protocol.
	Protocol string `json:"protocol"`
	// TLS reports whether the measured downstream scenario uses TLS.
	TLS bool `json:"tls"`
	// Verdict is the comparison's evidence/parity classification.
	Verdict Verdict `json:"verdict"`
	// Reasons contains deterministic human-readable verdict reasons.
	Reasons []string `json:"reasons"`
	// Direct summarizes the single required direct-control run.
	Direct DirectSummary `json:"direct"`
	// Go aggregates all G-Gateway runs.
	Go TargetSummary `json:"go"`
	// APISIX aggregates all APISIX runs.
	APISIX TargetSummary `json:"apisix"`
}

// DirectSummary describes upstream control capacity relative to the faster
// gateway target.
type DirectSummary struct {
	// RequestsPerSecond is throughput reparsed from direct-control evidence.
	RequestsPerSecond float64 `json:"requests_per_second"`
	// RequiredHeadroomFactor is the configured minimum direct/faster-target
	// ratio.
	RequiredHeadroomFactor float64 `json:"required_headroom_factor"`
	// HeadroomRatio is direct throughput divided by the faster target's median
	// throughput.
	HeadroomRatio float64 `json:"headroom_ratio"`
}

// TargetSummary aggregates repeated runs for one gateway target.
type TargetSummary struct {
	// RunCount is the number of runs; current evidence validation requires at
	// least one run per gateway target.
	RunCount int `json:"run_count"`
	// MedianRequestsPerSecond is the median of per-run throughput.
	MedianRequestsPerSecond float64 `json:"median_requests_per_second"`
	// MedianTransferBytesPerSecond is the median of per-run byte throughput.
	MedianTransferBytesPerSecond float64 `json:"median_transfer_bytes_per_second"`
	// MedianP50US is the median per-run p50 latency in microseconds.
	MedianP50US float64 `json:"median_p50_us"`
	// MedianP95US is the median per-run p95 latency in microseconds.
	MedianP95US float64 `json:"median_p95_us"`
	// MedianP99US is the median per-run p99 latency in microseconds.
	MedianP99US float64 `json:"median_p99_us"`
	// Requests is the total completed request count across runs.
	Requests int64 `json:"requests"`
	// RequestErrors is the total generator request-error count.
	RequestErrors int64 `json:"request_errors"`
	// Timeouts is the total timeout count.
	Timeouts int64 `json:"timeouts"`
	// Non2xx is the total non-2xx response count.
	Non2xx int64 `json:"non_2xx"`
	// ErrorRate is the sum of error, timeout, and non-2xx counts divided by
	// Requests.
	ErrorRate float64 `json:"error_rate"`
}

type rawRun struct {
	SchemaVersion    string `json:"schema_version"`
	TimestampUTC     string `json:"timestamp_utc"`
	EnvironmentClass string `json:"environment_class"`
	GatewayGitCommit string `json:"gateway_git_commit"`
	Target           struct {
		Name         string `json:"name"`
		Version      string `json:"version"`
		APISIXCommit string `json:"apisix_commit"`
	} `json:"target"`
	ImageIDs struct {
		Target    string `json:"target"`
		Upstream  string `json:"upstream"`
		Generator string `json:"generator"`
	} `json:"image_ids"`
	Scenario struct {
		Name      string `json:"name"`
		Generator string `json:"generator"`
		Protocol  string `json:"protocol"`
		TLS       bool   `json:"tls"`
	} `json:"scenario"`
	PayloadBytes      int64 `json:"payload_bytes"`
	GeneratorSettings struct {
		GeneratorVersion  string `json:"generator_version"`
		GeneratorRevision string `json:"generator_revision"`
		Threads           int    `json:"threads"`
		Connections       int    `json:"connections"`
		Clients           int    `json:"clients"`
		StreamsPerClient  int    `json:"streams_per_client"`
		WarmupSeconds     int    `json:"warmup_seconds"`
		DurationSeconds   int    `json:"duration_seconds"`
		Repetition        int    `json:"repetition"`
	} `json:"generator_settings"`
	TargetLimits struct {
		CPUs        float64 `json:"cpus"`
		MemoryBytes int64   `json:"memory_bytes"`
		Workers     int     `json:"workers"`
	} `json:"target_limits"`
	Environment struct {
		DockerVersion   string `json:"docker_version"`
		OperatingSystem string `json:"operating_system"`
		CPU             string `json:"cpu"`
	} `json:"environment"`
	DirectControl struct {
		RequestsPerSecond      float64 `json:"requests_per_second"`
		RequiredHeadroomFactor float64 `json:"required_headroom_factor"`
	} `json:"direct_control"`
	Artifacts struct {
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		Structured string `json:"structured"`
		RequestLog string `json:"request_log"`
	} `json:"artifacts"`
	Metrics metricSet `json:"metrics"`
	Errors  errorSet  `json:"errors"`
}

type metricSet struct {
	RequestsPerSecond      float64 `json:"requests_per_second"`
	TransferBytesPerSecond float64 `json:"transfer_bytes_per_second"`
	P50US                  float64 `json:"p50_us"`
	P95US                  float64 `json:"p95_us"`
	P99US                  float64 `json:"p99_us"`
}

type errorSet struct {
	RequestErrors int64 `json:"request_errors"`
	Timeouts      int64 `json:"timeouts"`
	Non2xx        int64 `json:"non_2xx"`
}

type parsedRun struct {
	raw      rawRun
	metrics  metricSet
	errors   errorSet
	requests int64
}

type comparisonKey struct {
	scenario string
	payload  int64
}

// Generate strictly validates and reparses benchmark artifacts, groups them by
// scenario and payload, uses nearest-rank h2load percentiles and medians across
// target runs, then writes deterministic JSON, CSV, and Markdown summaries.
// Each comparison requires exactly one direct run and at least one Go and
// APISIX run. Invalid aggregate measurements are written and returned with
// ErrInvalidEvidence; a provisional miss is returned without error. Malformed
// or missing input evidence returns its parsing error before reports are
// generated.
func Generate(opts Options) (Summary, error) {
	input, output, err := resolveOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	runs, err := readRuns(input)
	if err != nil {
		return Summary{}, err
	}
	summary, invalid := aggregate(runs)
	if err := writeReports(output, summary); err != nil {
		return Summary{}, err
	}
	if invalid {
		return summary, ErrInvalidEvidence
	}
	return summary, nil
}

func resolveOptions(opts Options) (string, string, error) {
	if strings.TrimSpace(opts.InputDir) == "" {
		return "", "", errors.New("input directory is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return "", "", errors.New("output directory is required")
	}
	input, err := filepath.Abs(opts.InputDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve input directory: %w", err)
	}
	info, err := os.Stat(input)
	if err != nil {
		return "", "", fmt.Errorf("stat input directory: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("input path %q is not a directory", input)
	}
	output, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve output directory: %w", err)
	}
	return input, output, nil
}

func readRuns(input string) ([]parsedRun, error) {
	var paths []string
	err := filepath.WalkDir(input, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "raw-run.json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk raw artifacts: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("no raw-run.json artifacts found")
	}
	sort.Strings(paths)

	runs := make([]parsedRun, 0, len(paths))
	seen := make(map[string]string, len(paths))
	for _, path := range paths {
		var raw rawRun
		if err := decodeStrictFile(path, &raw); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if err := validateRaw(raw); err != nil {
			return nil, fmt.Errorf("validate %s: %w", path, err)
		}
		identity := fmt.Sprintf("%s/%d/%s/%d", raw.Scenario.Name, raw.PayloadBytes, raw.Target.Name, raw.GeneratorSettings.Repetition)
		if previous, ok := seen[identity]; ok {
			return nil, fmt.Errorf("duplicate run %s in %s and %s", identity, previous, path)
		}
		seen[identity] = path
		parsed, err := parseRun(input, raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		runs = append(runs, parsed)
	}
	sort.Slice(runs, func(i, j int) bool {
		a, b := runs[i].raw, runs[j].raw
		if a.Scenario.Name != b.Scenario.Name {
			return a.Scenario.Name < b.Scenario.Name
		}
		if a.PayloadBytes != b.PayloadBytes {
			return a.PayloadBytes < b.PayloadBytes
		}
		if a.Target.Name != b.Target.Name {
			return a.Target.Name < b.Target.Name
		}
		return a.GeneratorSettings.Repetition < b.GeneratorSettings.Repetition
	})
	return runs, nil
}

func validateRaw(raw rawRun) error {
	if raw.SchemaVersion != "1.0.0" {
		return fmt.Errorf("unsupported schema_version %q", raw.SchemaVersion)
	}
	if raw.Scenario.Name == "" || raw.PayloadBytes < 0 || raw.GeneratorSettings.Repetition < 1 {
		return errors.New("scenario, payload, and repetition must be valid")
	}
	if raw.Target.Name != "direct" && raw.Target.Name != "go" && raw.Target.Name != "apisix" {
		return fmt.Errorf("unknown target %q", raw.Target.Name)
	}
	if raw.Scenario.Generator != "wrk" && raw.Scenario.Generator != "h2load" {
		return fmt.Errorf("unknown generator %q", raw.Scenario.Generator)
	}
	if raw.EnvironmentClass != EnvironmentProvisional && raw.EnvironmentClass != EnvironmentDedicatedLinux {
		return fmt.Errorf("unknown environment class %q", raw.EnvironmentClass)
	}
	if raw.DirectControl.RequiredHeadroomFactor <= 1 || !finiteNonNegative(raw.DirectControl.RequestsPerSecond) {
		return errors.New("direct control settings are invalid")
	}
	if raw.Errors.RequestErrors < 0 || raw.Errors.Timeouts < 0 || raw.Errors.Non2xx < 0 {
		return errors.New("raw error counts cannot be negative")
	}
	if raw.Artifacts.Stdout == "" || raw.Artifacts.Stderr == "" || raw.Artifacts.Structured == "" {
		return errors.New("required artifact path is empty")
	}
	if raw.Scenario.Generator == "h2load" && raw.Artifacts.RequestLog == "" {
		return errors.New("h2load request log path is empty")
	}
	return nil
}

func parseRun(input string, raw rawRun) (parsedRun, error) {
	for _, artifact := range []string{raw.Artifacts.Stdout, raw.Artifacts.Stderr} {
		if _, err := resolveArtifact(input, artifact); err != nil {
			return parsedRun{}, err
		}
	}
	structured, err := resolveArtifact(input, raw.Artifacts.Structured)
	if err != nil {
		return parsedRun{}, err
	}
	var metrics metricSet
	var parsedErrors errorSet
	var requests int64
	if raw.Scenario.Generator == "wrk" {
		metrics, parsedErrors, requests, err = parseWrk(structured)
	} else {
		requestLog, pathErr := resolveArtifact(input, raw.Artifacts.RequestLog)
		if pathErr != nil {
			return parsedRun{}, pathErr
		}
		metrics, parsedErrors, requests, err = parseH2Load(structured, requestLog)
	}
	if err != nil {
		return parsedRun{}, err
	}
	if parsedErrors != raw.Errors {
		return parsedRun{}, fmt.Errorf("raw errors %+v do not match generator errors %+v", raw.Errors, parsedErrors)
	}
	return parsedRun{raw: raw, metrics: metrics, errors: parsedErrors, requests: requests}, nil
}

func resolveArtifact(input, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("artifact path %q must be relative", relative)
	}
	path := filepath.Join(input, filepath.FromSlash(relative))
	rel, err := filepath.Rel(input, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q escapes input directory", relative)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat artifact %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", relative)
	}
	return path, nil
}

func parseWrk(path string) (metricSet, errorSet, int64, error) {
	var doc struct {
		SchemaVersion          string  `json:"schema_version"`
		Requests               int64   `json:"requests"`
		Bytes                  int64   `json:"bytes"`
		DurationUS             int64   `json:"duration_us"`
		RequestsPerSecond      float64 `json:"requests_per_second"`
		TransferBytesPerSecond float64 `json:"transfer_bytes_per_second"`
		P50US                  float64 `json:"p50_us"`
		P95US                  float64 `json:"p95_us"`
		P99US                  float64 `json:"p99_us"`
		Errors                 struct {
			Connect int64 `json:"connect"`
			Read    int64 `json:"read"`
			Write   int64 `json:"write"`
			Timeout int64 `json:"timeout"`
		} `json:"errors"`
		Non2xx int64 `json:"non_2xx"`
	}
	if err := decodeFile(path, &doc); err != nil {
		return metricSet{}, errorSet{}, 0, err
	}
	if doc.SchemaVersion != "wrk-1" || doc.Requests <= 0 || doc.DurationUS <= 0 {
		return metricSet{}, errorSet{}, 0, errors.New("invalid wrk structured artifact")
	}
	metrics := metricSet{doc.RequestsPerSecond, doc.TransferBytesPerSecond, doc.P50US, doc.P95US, doc.P99US}
	if !validMetrics(metrics) {
		return metricSet{}, errorSet{}, 0, errors.New("invalid wrk metrics")
	}
	errors := errorSet{doc.Errors.Connect + doc.Errors.Read + doc.Errors.Write, doc.Errors.Timeout, doc.Non2xx}
	return metrics, errors, doc.Requests, nil
}

func parseH2Load(structuredPath, requestLogPath string) (metricSet, errorSet, int64, error) {
	var doc struct {
		Version      string `json:"version"`
		Measurements struct {
			RequestPerSecond float64 `json:"request_per_second"`
			BytesPerSecond   float64 `json:"bytes_per_second"`
			Requests         struct {
				Total     int64 `json:"total"`
				Started   int64 `json:"started"`
				Done      int64 `json:"done"`
				Succeeded int64 `json:"succeeded"`
				Failed    int64 `json:"failed"`
				Errored   int64 `json:"errored"`
				Timeout   int64 `json:"timeout"`
			} `json:"requests"`
			StatusCodes struct {
				TwoXX   int64 `json:"2xx"`
				ThreeXX int64 `json:"3xx"`
				FourXX  int64 `json:"4xx"`
				FiveXX  int64 `json:"5xx"`
			} `json:"status_codes"`
		} `json:"measurements"`
	}
	if err := decodeFile(structuredPath, &doc); err != nil {
		return metricSet{}, errorSet{}, 0, err
	}
	if doc.Version != "v1" || doc.Measurements.Requests.Done <= 0 {
		return metricSet{}, errorSet{}, 0, errors.New("invalid h2load structured artifact")
	}
	durations, err := parseH2LoadRequestLog(requestLogPath)
	if err != nil {
		return metricSet{}, errorSet{}, 0, err
	}
	if int64(len(durations)) != doc.Measurements.Requests.Done {
		return metricSet{}, errorSet{}, 0, fmt.Errorf("h2load request log has %d records; structured output reports %d done", len(durations), doc.Measurements.Requests.Done)
	}
	metrics := metricSet{
		RequestsPerSecond:      doc.Measurements.RequestPerSecond,
		TransferBytesPerSecond: doc.Measurements.BytesPerSecond,
		P50US:                  nearestRank(durations, 50),
		P95US:                  nearestRank(durations, 95),
		P99US:                  nearestRank(durations, 99),
	}
	if !validMetrics(metrics) {
		return metricSet{}, errorSet{}, 0, errors.New("invalid h2load metrics")
	}
	errors := errorSet{
		RequestErrors: doc.Measurements.Requests.Failed + doc.Measurements.Requests.Errored,
		Timeouts:      doc.Measurements.Requests.Timeout,
		Non2xx:        doc.Measurements.StatusCodes.ThreeXX + doc.Measurements.StatusCodes.FourXX + doc.Measurements.StatusCodes.FiveXX,
	}
	return metrics, errors, doc.Measurements.Requests.Done, nil
}

func parseH2LoadRequestLog(path string) ([]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var durations []int64
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return nil, fmt.Errorf("request log line %d has %d fields; want 3", line, len(fields))
		}
		duration, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || duration < 0 {
			return nil, fmt.Errorf("request log line %d has invalid duration %q", line, fields[2])
		}
		durations = append(durations, duration)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(durations) == 0 {
		return nil, errors.New("h2load request log is empty")
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations, nil
}

func nearestRank(sorted []int64, percentile int) float64 {
	rank := int(math.Ceil(float64(percentile) / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return float64(sorted[rank-1])
}

func aggregate(runs []parsedRun) (Summary, bool) {
	groups := make(map[comparisonKey][]parsedRun)
	environment := EnvironmentDedicatedLinux
	for _, run := range runs {
		key := comparisonKey{run.raw.Scenario.Name, run.raw.PayloadBytes}
		groups[key] = append(groups[key], run)
		if run.raw.EnvironmentClass != EnvironmentDedicatedLinux || strings.Contains(strings.ToLower(run.raw.Environment.OperatingSystem), "docker desktop") {
			environment = EnvironmentProvisional
		}
	}
	keys := make([]comparisonKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].scenario != keys[j].scenario {
			return keys[i].scenario < keys[j].scenario
		}
		return keys[i].payload < keys[j].payload
	})

	summary := Summary{SchemaVersion: "1.0.0", EnvironmentClass: environment}
	invalidOverall := false
	missOverall := false
	for _, key := range keys {
		group := groups[key]
		comparison := compareGroup(key, group)
		summary.Comparisons = append(summary.Comparisons, comparison)
		switch comparison.Verdict {
		case VerdictInvalid:
			invalidOverall = true
		case VerdictProvisionalMiss:
			missOverall = true
		}
	}
	switch {
	case invalidOverall:
		summary.Verdict = VerdictInvalid
	case missOverall:
		summary.Verdict = VerdictProvisionalMiss
	default:
		summary.Verdict = VerdictProvisionalPass
	}
	return summary, invalidOverall
}

func compareGroup(key comparisonKey, runs []parsedRun) Comparison {
	comparison := Comparison{Scenario: key.scenario, PayloadBytes: key.payload}
	var directRuns, goRuns, apisixRuns []parsedRun
	for _, run := range runs {
		comparison.Generator = run.raw.Scenario.Generator
		comparison.Protocol = run.raw.Scenario.Protocol
		comparison.TLS = run.raw.Scenario.TLS
		switch run.raw.Target.Name {
		case "direct":
			directRuns = append(directRuns, run)
		case "go":
			goRuns = append(goRuns, run)
		case "apisix":
			apisixRuns = append(apisixRuns, run)
		}
	}
	if len(directRuns) != 1 || len(goRuns) == 0 || len(apisixRuns) == 0 {
		comparison.Verdict = VerdictInvalid
		comparison.Reasons = []string{fmt.Sprintf("expected one direct run and at least one run per gateway; found direct=%d go=%d apisix=%d", len(directRuns), len(goRuns), len(apisixRuns))}
		return comparison
	}
	comparison.Go = aggregateTarget(goRuns)
	comparison.APISIX = aggregateTarget(apisixRuns)
	direct := directRuns[0]
	fastest := math.Max(comparison.Go.MedianRequestsPerSecond, comparison.APISIX.MedianRequestsPerSecond)
	comparison.Direct = DirectSummary{
		RequestsPerSecond:      direct.metrics.RequestsPerSecond,
		RequiredHeadroomFactor: direct.raw.DirectControl.RequiredHeadroomFactor,
		HeadroomRatio:          direct.metrics.RequestsPerSecond / fastest,
	}

	var invalidReasons []string
	if direct.errors.RequestErrors+direct.errors.Timeouts+direct.errors.Non2xx > 0 {
		invalidReasons = append(invalidReasons, "direct control reported request errors, timeouts, or non-2xx responses")
	}
	if comparison.Go.RequestErrors+comparison.Go.Timeouts+comparison.Go.Non2xx > 0 || comparison.APISIX.RequestErrors+comparison.APISIX.Timeouts+comparison.APISIX.Non2xx > 0 {
		invalidReasons = append(invalidReasons, "gateway measurement reported request errors, timeouts, or non-2xx responses")
	}
	if comparison.Direct.HeadroomRatio < comparison.Direct.RequiredHeadroomFactor {
		invalidReasons = append(invalidReasons, fmt.Sprintf("direct headroom %.4fx is below required %.4fx", comparison.Direct.HeadroomRatio, comparison.Direct.RequiredHeadroomFactor))
	}
	if len(invalidReasons) > 0 {
		comparison.Verdict = VerdictInvalid
		comparison.Reasons = invalidReasons
		return comparison
	}

	throughputPass := comparison.Go.MedianRequestsPerSecond >= comparison.APISIX.MedianRequestsPerSecond
	latencyPass := comparison.Go.MedianP99US <= comparison.APISIX.MedianP99US*1.10
	errorPass := comparison.Go.ErrorRate <= comparison.APISIX.ErrorRate
	if throughputPass && latencyPass && errorPass {
		comparison.Verdict = VerdictProvisionalPass
		comparison.Reasons = []string{"Go meets throughput, p99, and error-rate parity thresholds"}
		return comparison
	}
	comparison.Verdict = VerdictProvisionalMiss
	if !throughputPass {
		comparison.Reasons = append(comparison.Reasons, "Go median throughput is below APISIX")
	}
	if !latencyPass {
		comparison.Reasons = append(comparison.Reasons, "Go median p99 exceeds 110% of APISIX")
	}
	if !errorPass {
		comparison.Reasons = append(comparison.Reasons, "Go error rate exceeds APISIX")
	}
	return comparison
}

func aggregateTarget(runs []parsedRun) TargetSummary {
	rps := make([]float64, 0, len(runs))
	transfer := make([]float64, 0, len(runs))
	p50 := make([]float64, 0, len(runs))
	p95 := make([]float64, 0, len(runs))
	p99 := make([]float64, 0, len(runs))
	result := TargetSummary{RunCount: len(runs)}
	for _, run := range runs {
		rps = append(rps, run.metrics.RequestsPerSecond)
		transfer = append(transfer, run.metrics.TransferBytesPerSecond)
		p50 = append(p50, run.metrics.P50US)
		p95 = append(p95, run.metrics.P95US)
		p99 = append(p99, run.metrics.P99US)
		result.Requests += run.requests
		result.RequestErrors += run.errors.RequestErrors
		result.Timeouts += run.errors.Timeouts
		result.Non2xx += run.errors.Non2xx
	}
	result.MedianRequestsPerSecond = median(rps)
	result.MedianTransferBytesPerSecond = median(transfer)
	result.MedianP50US = median(p50)
	result.MedianP95US = median(p95)
	result.MedianP99US = median(p99)
	if result.Requests > 0 {
		result.ErrorRate = float64(result.RequestErrors+result.Timeouts+result.Non2xx) / float64(result.Requests)
	}
	return result
}

func median(values []float64) float64 {
	copyOfValues := append([]float64(nil), values...)
	sort.Float64s(copyOfValues)
	middle := len(copyOfValues) / 2
	if len(copyOfValues)%2 == 1 {
		return copyOfValues[middle]
	}
	return (copyOfValues[middle-1] + copyOfValues[middle]) / 2
}

func writeReports(output string, summary Summary) error {
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	jsonData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary JSON: %w", err)
	}
	jsonData = append(jsonData, '\n')
	if err := os.WriteFile(filepath.Join(output, "summary.json"), jsonData, 0o644); err != nil {
		return fmt.Errorf("write summary JSON: %w", err)
	}
	if err := writeCSV(filepath.Join(output, "summary.csv"), summary); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "summary.md"), []byte(renderMarkdown(summary)), 0o644); err != nil {
		return fmt.Errorf("write summary Markdown: %w", err)
	}
	return nil
}

func writeCSV(path string, summary Summary) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create summary CSV: %w", err)
	}
	writer := csv.NewWriter(file)
	rows := [][]string{{
		"environment_class", "overall_verdict", "scenario", "payload_bytes", "generator", "protocol", "tls", "verdict",
		"direct_rps", "headroom_ratio", "go_median_rps", "apisix_median_rps", "go_median_p99_us", "apisix_median_p99_us",
		"go_error_rate", "apisix_error_rate", "reasons",
	}}
	for _, comparison := range summary.Comparisons {
		rows = append(rows, []string{
			summary.EnvironmentClass, string(summary.Verdict), comparison.Scenario, strconv.FormatInt(comparison.PayloadBytes, 10),
			comparison.Generator, comparison.Protocol, strconv.FormatBool(comparison.TLS), string(comparison.Verdict),
			formatFloat(comparison.Direct.RequestsPerSecond), formatFloat(comparison.Direct.HeadroomRatio),
			formatFloat(comparison.Go.MedianRequestsPerSecond), formatFloat(comparison.APISIX.MedianRequestsPerSecond),
			formatFloat(comparison.Go.MedianP99US), formatFloat(comparison.APISIX.MedianP99US),
			formatFloat(comparison.Go.ErrorRate), formatFloat(comparison.APISIX.ErrorRate), strings.Join(comparison.Reasons, "; "),
		})
	}
	writeErr := writer.WriteAll(rows)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write summary CSV: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close summary CSV: %w", closeErr)
	}
	return nil
}

func renderMarkdown(summary Summary) string {
	var out strings.Builder
	fmt.Fprintln(&out, "# Benchmark summary")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "- Environment: `%s`\n", summary.EnvironmentClass)
	fmt.Fprintf(&out, "- Verdict: `%s`\n\n", summary.Verdict)
	fmt.Fprintln(&out, "| Scenario | Payload | Verdict | Direct RPS | Headroom | Go RPS | APISIX RPS | Go p99 (us) | APISIX p99 (us) |")
	fmt.Fprintln(&out, "|---|---:|---|---:|---:|---:|---:|---:|---:|")
	for _, comparison := range summary.Comparisons {
		fmt.Fprintf(&out, "| %s | %d | %s | %.2f | %.2fx | %.2f | %.2f | %.2f | %.2f |\n",
			comparison.Scenario, comparison.PayloadBytes, comparison.Verdict, comparison.Direct.RequestsPerSecond,
			comparison.Direct.HeadroomRatio, comparison.Go.MedianRequestsPerSecond, comparison.APISIX.MedianRequestsPerSecond,
			comparison.Go.MedianP99US, comparison.APISIX.MedianP99US)
	}
	fmt.Fprintln(&out)
	for _, comparison := range summary.Comparisons {
		fmt.Fprintf(&out, "- `%s/%d`: %s\n", comparison.Scenario, comparison.PayloadBytes, strings.Join(comparison.Reasons, "; "))
	}
	return out.String()
}

func decodeStrictFile(path string, destination any) error {
	return decodeFileWithMode(path, destination, true)
}

func decodeFile(path string, destination any) error {
	return decodeFileWithMode(path, destination, false)
}

func decodeFileWithMode(path string, destination any, strict bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validMetrics(metrics metricSet) bool {
	return finiteNonNegative(metrics.RequestsPerSecond) && finiteNonNegative(metrics.TransferBytesPerSecond) &&
		finiteNonNegative(metrics.P50US) && finiteNonNegative(metrics.P95US) && finiteNonNegative(metrics.P99US)
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 9, 64)
}
