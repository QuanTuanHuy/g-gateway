// Package main implements the bench-report command, which validates benchmark
// evidence and renders deterministic comparison summaries and verdicts.
package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/QuanTuanHuy/g-gateway/internal/benchreport"
)

func main() {
	input := flag.String("input", "", "directory containing raw benchmark artifacts")
	output := flag.String("output", "", "directory for deterministic summary artifacts")
	flag.Parse()

	summary, err := benchreport.Generate(benchreport.Options{InputDir: *input, OutputDir: *output})
	if summary.SchemaVersion != "" {
		for _, name := range []string{"summary.json", "summary.csv", "summary.md"} {
			fmt.Println(filepath.Join(*output, name))
		}
	}
	if err != nil {
		log.Fatal(err)
	}
}
