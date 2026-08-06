package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tshoot-browser-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "path to a versioned redacted browser benchmark corpus JSON")
	collectRoot := flags.String("collect-root", "", "Studio workflow root to collect with read-only access")
	outputPath := flags.String("output", "", "output path for a collected redacted corpus")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || (*corpusPath == "") == (*collectRoot == "") {
		fmt.Fprintln(stderr, "exactly one of -corpus or -collect-root is required; positional arguments are not supported")
		return 1
	}
	if *collectRoot != "" {
		if *outputPath == "" {
			fmt.Fprintln(stderr, "-output is required with -collect-root")
			return 1
		}
		collection, err := bughub.CollectBrowserDecisionBenchmarkFromRoot(context.Background(), *collectRoot)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		encoded, err := json.MarshalIndent(collection.Corpus, "", "  ")
		if err != nil || len(encoded) > bughub.MaxBrowserDecisionBenchmarkCorpusBytes {
			fmt.Fprintln(stderr, "encode collected corpus failed")
			return 1
		}
		if err := writeBenchmarkCorpusAtomic(*outputPath, append(encoded, '\n')); err != nil {
			fmt.Fprintf(stderr, "write collected corpus: %v\n", err)
			return 1
		}
		summary := struct {
			Version            int              `json:"version"`
			ScannedAttempts    int64            `json:"scanned_attempts"`
			EligibleAttempts   int64            `json:"eligible_attempts"`
			AutonomousAttempts int64            `json:"autonomous_attempts"`
			CollectedSamples   int64            `json:"collected_samples"`
			Skipped            map[string]int64 `json:"skipped"`
		}{collection.Version, collection.ScannedAttempts, collection.EligibleAttempts, collection.AutonomousAttempts, collection.CollectedSamples, collection.Skipped}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(summary); err != nil {
			fmt.Fprintf(stderr, "encode collection summary: %v\n", err)
			return 1
		}
		return 0
	}
	if *outputPath != "" {
		fmt.Fprintln(stderr, "-output is supported only with -collect-root")
		return 1
	}
	file, err := os.Open(*corpusPath)
	if err != nil {
		fmt.Fprintf(stderr, "open corpus: %v\n", err)
		return 1
	}
	content, readErr := io.ReadAll(io.LimitReader(file, bughub.MaxBrowserDecisionBenchmarkCorpusBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		fmt.Fprintf(stderr, "read corpus: %v\n", errorsJoin(readErr, closeErr))
		return 1
	}
	corpus, err := bughub.DecodeBrowserDecisionBenchmarkCorpus(content)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output, err := bughub.RunBrowserDecisionBenchmarkCorpus(corpus)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 1
	}
	if !output.Report.Passed {
		return 2
	}
	return 0
}

func writeBenchmarkCorpusAtomic(path string, content []byte) error {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return errorsJoin(errors.New("output parent is unavailable"), err)
	}
	temporary, err := os.CreateTemp(parent, ".browser-benchmark-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleaned := false
	defer func() {
		if !cleaned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleaned = true
	return nil
}

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
