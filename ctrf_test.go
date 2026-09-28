package flakemerge

import (
	"encoding/json"
	"testing"
)

func TestParseCTRF_MalformedJSON(t *testing.T) {
	_, err := ParseCTRF(readTestdata(t, "ctrf/malformed.json"), 1)
	if err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

// TestCTRFRoundTrip_FlakyScenario is the same double-count scenario as
// TestJUnitRoundTrip_FlakyScenario, but for CTRF input/output: attempt 1
// fails testFlaky, attempt 2 passes it. junit-report-merger and ctrf-cli
// both leave two entries (one failed, one passed) and report it failed
// overall - see README Why. This package must merge it into one flaky,
// passing entry with retries recorded.
func TestCTRFRoundTrip_FlakyScenario(t *testing.T) {
	a1, err := ParseCTRF(readTestdata(t, "ctrf/attempt1.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := ParseCTRF(readTestdata(t, "ctrf/attempt2.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Timestamp == nil {
		t.Fatal("expected ParseCTRF to capture summary.start as a timestamp")
	}

	merged := MergeAll([]AttemptReport{a1, a2}, KeyClassnameName)
	var flaky, stable *MergedTest
	for i := range merged {
		switch merged[i].Identity.Name {
		case "testFlaky":
			flaky = &merged[i]
		case "testStable":
			stable = &merged[i]
		}
	}
	if flaky == nil || flaky.Outcome != OutcomeFlaky {
		t.Fatalf("testFlaky = %+v, want Outcome Flaky", flaky)
	}
	if flaky.Retries != 1 {
		t.Fatalf("testFlaky.Retries = %d, want 1", flaky.Retries)
	}
	if stable == nil || stable.Outcome != OutcomePassed {
		t.Fatalf("testStable = %+v, want Outcome Passed", stable)
	}

	out, err := WriteCTRF(merged, *a1.Timestamp)
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	results := doc["results"].(map[string]any)
	summary := results["summary"].(map[string]any)
	if summary["failed"].(float64) != 0 {
		t.Fatalf("summary.failed = %v, want 0 (the flaky test must not count as failed)", summary["failed"])
	}
	if summary["flaky"].(float64) != 1 {
		t.Fatalf("summary.flaky = %v, want 1", summary["flaky"])
	}

	tests := results["tests"].([]any)
	if len(tests) != 2 {
		t.Fatalf("got %d tests in output, want 2 (no double-counted duplicate)", len(tests))
	}
	for _, raw := range tests {
		tc := raw.(map[string]any)
		if tc["name"] == "testFlaky" {
			if tc["status"] != "passed" {
				t.Fatalf(`testFlaky status = %v, want "passed"`, tc["status"])
			}
			if tc["flaky"] != true {
				t.Fatalf("testFlaky.flaky = %v, want true", tc["flaky"])
			}
			retries, ok := tc["retryAttempts"].([]any)
			if !ok || len(retries) != 1 {
				t.Fatalf("testFlaky.retryAttempts = %v, want one recorded prior attempt", tc["retryAttempts"])
			}
		}
	}
}

func TestParseCTRF_AcceptsBareStringSuite(t *testing.T) {
	data := []byte(`{"results":{"tool":{"name":"x"},"summary":{"tests":1,"passed":1,"failed":0,"skipped":0,"pending":0,"other":0,"start":0,"stop":0},"tests":[{"name":"t","status":"passed","duration":10,"suite":"NotAnArray"}]}}`)
	report, err := ParseCTRF(data, 1)
	if err != nil {
		t.Fatalf("ParseCTRF should tolerate a bare string suite: %v", err)
	}
	if report.Cases[0].Identity.Suite != "NotAnArray" {
		t.Fatalf("Suite = %q, want %q", report.Cases[0].Identity.Suite, "NotAnArray")
	}
}
