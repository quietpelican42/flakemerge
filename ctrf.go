package flakemerge

import (
	"encoding/json"
	"fmt"
	"time"
)

// CTRF (Common Test Report Format, https://ctrf.io) types, matching the
// fields this package reads and writes from schema/ctrf.schema.json in
// ctrf-io/ctrf (commit 66e823ca2c9e1f54bf8b387adda0b7e0c7610538). Only the
// subset used for merging is modelled; unknown fields in input are ignored.

type ctrfReport struct {
	ReportFormat string      `json:"reportFormat"`
	SpecVersion  string      `json:"specVersion"`
	Results      ctrfResults `json:"results"`
}

type ctrfResults struct {
	Tool    ctrfTool    `json:"tool"`
	Summary ctrfSummary `json:"summary"`
	Tests   []ctrfTest  `json:"tests"`
}

type ctrfTool struct {
	Name string `json:"name"`
}

type ctrfSummary struct {
	Tests   int   `json:"tests"`
	Passed  int   `json:"passed"`
	Failed  int   `json:"failed"`
	Skipped int   `json:"skipped"`
	Pending int   `json:"pending"`
	Other   int   `json:"other"`
	Flaky   int   `json:"flaky,omitempty"`
	Start   int64 `json:"start"`
	Stop    int64 `json:"stop"`
}

type ctrfTest struct {
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	Duration      int64              `json:"duration"`
	Suite         ctrfSuite          `json:"suite,omitempty"`
	FilePath      string             `json:"filePath,omitempty"`
	Message       string             `json:"message,omitempty"`
	Trace         string             `json:"trace,omitempty"`
	RawStatus     string             `json:"rawStatus,omitempty"`
	Retries       int                `json:"retries,omitempty"`
	Flaky         bool               `json:"flaky,omitempty"`
	RetryAttempts []ctrfRetryAttempt `json:"retryAttempts,omitempty"`
}

// ctrfSuite is the CTRF schema's "suite" field: an array of strings from
// top-level suite to immediate parent. This package's internal model only
// tracks one suite/classname per test, so it reads and writes a
// single-element array; on input it also accepts a bare string, which some
// non-conformant producers emit instead of the array the schema requires.
type ctrfSuite []string

func (s ctrfSuite) name() string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func newCTRFSuite(name string) ctrfSuite {
	if name == "" {
		return nil
	}
	return ctrfSuite{name}
}

func (s *ctrfSuite) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	if one == "" {
		*s = nil
	} else {
		*s = ctrfSuite{one}
	}
	return nil
}

type ctrfRetryAttempt struct {
	Attempt  int    `json:"attempt"`
	Status   string `json:"status"`
	Duration int64  `json:"duration,omitempty"`
	Message  string `json:"message,omitempty"`
	Trace    string `json:"trace,omitempty"`
}

// ctrfStatusToStatus maps a CTRF status (plus optional rawStatus) onto our
// internal Status. CTRF has no "error" outcome distinct from "failed"; when
// rawStatus says "error" (as this package's own writer does) that
// distinction is recovered, otherwise a failed test is StatusFailed.
// "pending" (not yet run) and "other" (inconclusive) both map to Skipped:
// neither is a pass or a fail, and skipped is the closest fit for "did not
// produce a real result".
func ctrfStatusToStatus(status, rawStatus string) Status {
	if rawStatus == "error" {
		return StatusError
	}
	switch status {
	case "passed":
		return StatusPassed
	case "failed":
		return StatusFailed
	default: // "skipped", "pending", "other"
		return StatusSkipped
	}
}

func statusToCTRF(s Status) (status, rawStatus string) {
	switch s {
	case StatusPassed:
		return "passed", ""
	case StatusFailed:
		return "failed", "failed"
	case StatusError:
		return "failed", "error"
	default:
		return "skipped", ""
	}
}

// ParseCTRF parses one CTRF JSON attempt file into an AttemptReport. It
// returns an error for malformed JSON or a document missing the required
// "results" object; callers should treat that as bad input (exit code 2).
func ParseCTRF(data []byte, attempt int) (AttemptReport, error) {
	var doc ctrfReport
	if err := json.Unmarshal(data, &doc); err != nil {
		return AttemptReport{}, fmt.Errorf("parse CTRF JSON: %w", err)
	}
	if doc.Results.Tests == nil && doc.Results.Summary.Tests == 0 && doc.Results.Tool.Name == "" {
		return AttemptReport{}, fmt.Errorf("parse CTRF JSON: missing or empty \"results\" object")
	}

	report := AttemptReport{Attempt: attempt}
	if doc.Results.Summary.Start > 0 {
		t := time.UnixMilli(doc.Results.Summary.Start).UTC()
		report.Timestamp = &t
	}

	for _, tc := range doc.Results.Tests {
		id := Identity{Suite: tc.Suite.name(), Name: tc.Name, File: tc.FilePath}

		var prior []Event
		for _, ra := range tc.RetryAttempts {
			prior = append(prior, Event{
				Status:  ctrfStatusToStatus(ra.Status, ""),
				Message: ra.Message,
				Body:    ra.Trace,
				Time:    float64(ra.Duration) / 1000.0,
			})
		}

		final := Event{
			Status:  ctrfStatusToStatus(tc.Status, tc.RawStatus),
			Message: tc.Message,
			Body:    tc.Trace,
			Time:    float64(tc.Duration) / 1000.0,
		}
		if final.Status == StatusError || final.Status == StatusFailed {
			final.Type = tc.RawStatus
		}

		report.Cases = append(report.Cases, ParsedCase{Identity: id, Events: append(prior, final)})
	}
	return report, nil
}

// WriteCTRF renders merged tests as a CTRF JSON report. start gives the
// report's start time; if zero, a synthetic zero-based timeline is used
// (start=0, stop=total measured duration) since JUnit inputs frequently
// carry no reliable wall-clock timestamp for individual tests.
func WriteCTRF(tests []MergedTest, start time.Time) ([]byte, error) {
	doc := ctrfReport{
		ReportFormat: "CTRF",
		SpecVersion:  "1.0.0",
		Results: ctrfResults{
			Tool: ctrfTool{Name: "flakemerge"},
		},
	}

	var totalMs int64
	for _, t := range tests {
		status, rawStatus := statusToCTRF(t.FinalStatus)
		ct := ctrfTest{
			Name:      t.Identity.Name,
			Suite:     newCTRFSuite(t.Identity.Suite),
			FilePath:  t.Identity.File,
			Status:    status,
			RawStatus: rawStatus,
			Duration:  int64(t.LastTime * 1000),
			Retries:   t.Retries,
			Flaky:     t.Outcome == OutcomeFlaky,
		}
		if t.Outcome == OutcomeFailed || t.Outcome == OutcomeSkipped {
			ct.Message = t.Primary.Message
			ct.Trace = t.Primary.Body
		}
		for i, e := range t.Prior {
			s, _ := statusToCTRF(e.Status)
			ct.RetryAttempts = append(ct.RetryAttempts, ctrfRetryAttempt{
				Attempt:  i + 1,
				Status:   s,
				Duration: int64(e.Time * 1000),
				Message:  e.Message,
				Trace:    e.Body,
			})
		}
		doc.Results.Tests = append(doc.Results.Tests, ct)
		totalMs += int64(t.TotalTime * 1000)
	}

	summary := Summarize(tests)
	doc.Results.Summary = ctrfSummary{
		Tests:   summary.Total,
		Passed:  summary.Passed + summary.Flaky,
		Failed:  summary.Failed,
		Skipped: summary.Skipped,
		Flaky:   summary.Flaky,
	}
	if !start.IsZero() {
		doc.Results.Summary.Start = start.UnixMilli()
		doc.Results.Summary.Stop = start.UnixMilli() + totalMs
	} else {
		doc.Results.Summary.Start = 0
		doc.Results.Summary.Stop = totalMs
	}

	return json.MarshalIndent(doc, "", "  ")
}
