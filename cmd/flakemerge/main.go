// Command flakemerge merges JUnit XML or CTRF JSON test reports from
// multiple CI retry attempts into one report with correct flaky-versus-
// failed semantics.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/__GH_OWNER__/flakemerge"
)

const usage = `flakemerge merges JUnit XML or CTRF JSON test reports from repeated CI
attempts (retries) into one report: a test that failed then later passed is
flaky, not failed.

Usage:
  flakemerge [flags] --attempt N file [--attempt N file ...]
  flakemerge [flags] glob [glob ...]

In the second form, each glob is one attempt, numbered in the order given
(the first glob is attempt 1); every file the glob matches belongs to that
attempt.

Flags:
  --key MODE        Test identity: classname (default), name, or
                     classname+file. See README Reference.
  --out-junit PATH   Write merged JUnit XML to PATH.
  --out-ctrf PATH    Write merged CTRF JSON to PATH.
  --out-summary PATH Write the text summary to PATH (default: stdout).
  -h, --help         Show this help.

Exit codes: 0 no hard failures, 1 one or more tests failed, 2 bad input.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var (
		keyMode                       = flakemerge.KeyClassnameName
		outJUnit, outCTRF, outSummary string
		explicitAttempts              []explicitAttempt
		globs                         []string
	)
	type kv = explicitAttempt

	i := 0
	next := func(flag string) (string, bool) {
		i++
		if i >= len(args) {
			fmt.Fprintf(os.Stderr, "flakemerge: %s requires a value\n", flag)
			return "", false
		}
		return args[i], true
	}

	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprint(os.Stdout, usage)
			return 0
		case a == "--key":
			v, ok := next(a)
			if !ok {
				return 2
			}
			m, err := flakemerge.ParseKeyMode(v)
			if err != nil {
				fmt.Fprintf(os.Stderr, "flakemerge: %v\n", err)
				return 2
			}
			keyMode = m
		case a == "--out-junit":
			v, ok := next(a)
			if !ok {
				return 2
			}
			outJUnit = v
		case a == "--out-ctrf":
			v, ok := next(a)
			if !ok {
				return 2
			}
			outCTRF = v
		case a == "--out-summary":
			v, ok := next(a)
			if !ok {
				return 2
			}
			outSummary = v
		case a == "--attempt":
			numStr, ok := next(a)
			if !ok {
				return 2
			}
			num, err := strconv.Atoi(numStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "flakemerge: --attempt wants an integer, got %q\n", numStr)
				return 2
			}
			file, ok := next(a)
			if !ok {
				return 2
			}
			explicitAttempts = append(explicitAttempts, kv{attempt: num, path: file})
		case strings.HasPrefix(a, "-") && a != "-":
			fmt.Fprintf(os.Stderr, "flakemerge: unknown flag %q\n", a)
			return 2
		default:
			globs = append(globs, a)
		}
	}

	if len(explicitAttempts) > 0 && len(globs) > 0 {
		fmt.Fprintln(os.Stderr, "flakemerge: use either --attempt flags or positional globs, not both")
		return 2
	}
	if len(explicitAttempts) == 0 && len(globs) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	var files []attemptFile
	if len(explicitAttempts) > 0 {
		for _, ea := range explicitAttempts {
			files = append(files, attemptFile{attempt: ea.attempt, path: ea.path})
		}
	} else {
		for idx, g := range globs {
			matches, err := filepath.Glob(g)
			if err != nil {
				fmt.Fprintf(os.Stderr, "flakemerge: bad glob %q: %v\n", g, err)
				return 2
			}
			if len(matches) == 0 {
				fmt.Fprintf(os.Stderr, "flakemerge: glob %q matched no files\n", g)
				return 2
			}
			sort.Strings(matches)
			for _, m := range matches {
				files = append(files, attemptFile{attempt: idx + 1, path: m})
			}
		}
	}

	var reports []flakemerge.AttemptReport
	for _, f := range files {
		data, err := os.ReadFile(f.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: read %s: %v\n", f.path, err)
			return 2
		}
		report, err := parseAny(data, f.attempt, f.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: %s: %v\n", f.path, err)
			return 2
		}
		reports = append(reports, report)
	}

	merged := flakemerge.MergeAll(reports, keyMode)
	start := earliestTimestamp(reports)
	generatedAt := start
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}

	if outJUnit != "" {
		data, err := flakemerge.WriteJUnit(merged, generatedAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: render JUnit: %v\n", err)
			return 2
		}
		if err := os.WriteFile(outJUnit, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: write %s: %v\n", outJUnit, err)
			return 2
		}
	}
	if outCTRF != "" {
		data, err := flakemerge.WriteCTRF(merged, start)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: render CTRF: %v\n", err)
			return 2
		}
		if err := os.WriteFile(outCTRF, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: write %s: %v\n", outCTRF, err)
			return 2
		}
	}

	var summaryOut = os.Stdout
	if outSummary != "" {
		f, err := os.Create(outSummary)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flakemerge: write %s: %v\n", outSummary, err)
			return 2
		}
		defer f.Close()
		summaryOut = f
	}
	if err := flakemerge.WriteTextSummary(summaryOut, merged); err != nil {
		fmt.Fprintf(os.Stderr, "flakemerge: write summary: %v\n", err)
		return 2
	}

	return flakemerge.ExitCode(flakemerge.Summarize(merged))
}

type explicitAttempt struct {
	attempt int
	path    string
}

type attemptFile struct {
	attempt int
	path    string
}

// parseAny sniffs the file's dialect from its content (leading '<' or '{')
// rather than trusting the extension, so a misnamed file is not silently
// misread.
func parseAny(data []byte, attempt int, path string) (flakemerge.AttemptReport, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // strip a UTF-8 BOM if present
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "<"):
		return flakemerge.ParseJUnit(data, attempt)
	case strings.HasPrefix(trimmed, "{"):
		return flakemerge.ParseCTRF(data, attempt)
	default:
		return flakemerge.AttemptReport{}, fmt.Errorf("unrecognized file format (not XML or JSON)")
	}
}

func earliestTimestamp(reports []flakemerge.AttemptReport) time.Time {
	var earliest time.Time
	for _, r := range reports {
		if r.Timestamp == nil {
			continue
		}
		if earliest.IsZero() || r.Timestamp.Before(earliest) {
			earliest = *r.Timestamp
		}
	}
	return earliest
}
