package flakemerge

import (
	"os"
	"path/filepath"
	"testing"
)

func readTestdata(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("read testdata %s: %v", rel, err)
	}
	return data
}

// TestParseJUnit_Dialects checks that every dialect this package claims to
// support (Surefire/Ant, pytest xunit2, jest-junit, go-junit-report/
// gotestsum) parses into the same shape: the right number of cases, right
// pass/fail/skip classification.
func TestParseJUnit_Dialects(t *testing.T) {
	cases := []struct {
		file          string
		wantCases     int
		wantFailed    int
		wantSkipped   int
		firstCaseName string
	}{
		{"junit/surefire.xml", 2, 1, 0, "testAdd"},
		{"junit/pytest_xunit2.xml", 3, 1, 1, "test_get"},
		{"junit/jest_junit.xml", 1, 0, 0, "renders without crashing"},
		{"junit/go_junit_report.xml", 2, 1, 0, "TestAdd"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data := readTestdata(t, tc.file)
			report, err := ParseJUnit(data, 1)
			if err != nil {
				t.Fatalf("ParseJUnit: %v", err)
			}
			if len(report.Cases) != tc.wantCases {
				t.Fatalf("got %d cases, want %d", len(report.Cases), tc.wantCases)
			}
			if report.Cases[0].Identity.Name != tc.firstCaseName {
				t.Fatalf("first case name = %q, want %q", report.Cases[0].Identity.Name, tc.firstCaseName)
			}
			var failed, skipped int
			for _, c := range report.Cases {
				final := c.Events[len(c.Events)-1]
				switch final.Status {
				case StatusFailed, StatusError:
					failed++
				case StatusSkipped:
					skipped++
				}
			}
			if failed != tc.wantFailed {
				t.Fatalf("got %d failed, want %d", failed, tc.wantFailed)
			}
			if skipped != tc.wantSkipped {
				t.Fatalf("got %d skipped, want %d", skipped, tc.wantSkipped)
			}
		})
	}
}

func TestParseJUnit_MalformedXML(t *testing.T) {
	data := readTestdata(t, "junit/malformed.xml")
	_, err := ParseJUnit(data, 1)
	if err == nil {
		t.Fatal("expected an error for malformed XML, got nil")
	}
}

func TestParseJUnit_NativeFlakyFailureInput(t *testing.T) {
	data := readTestdata(t, "junit/surefire_flaky_input.xml")
	report, err := ParseJUnit(data, 1)
	if err != nil {
		t.Fatalf("ParseJUnit: %v", err)
	}
	if len(report.Cases) != 1 {
		t.Fatalf("got %d cases, want 1", len(report.Cases))
	}
	events := report.Cases[0].Events
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (the embedded flakyFailure, then the final pass)", len(events))
	}
	if events[0].Status != StatusFailed || events[0].Message != "timeout" {
		t.Fatalf("events[0] = %+v, want the flakyFailure's failed event", events[0])
	}
	if events[1].Status != StatusPassed {
		t.Fatalf("events[1].Status = %v, want Passed (the outer testcase result)", events[1].Status)
	}

	// Feeding this single already-merged file through our own merge engine
	// as a single "attempt" must still resolve to Flaky.
	occs := BuildOccurrences([]AttemptReport{report}, KeyClassnameName)
	mt := Merge(occs[0])
	if mt.Outcome != OutcomeFlaky {
		t.Fatalf("Outcome = %v, want Flaky", mt.Outcome)
	}
}

func TestParseJUnit_NativeRerunFailureInput(t *testing.T) {
	data := readTestdata(t, "junit/surefire_rerun_input.xml")
	report, err := ParseJUnit(data, 1)
	if err != nil {
		t.Fatalf("ParseJUnit: %v", err)
	}
	events := report.Cases[0].Events
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (embedded rerunFailure, then the final failure)", len(events))
	}
	if events[0].Message != "attempt 1 failed" || events[1].Message != "attempt 2 failed" {
		t.Fatalf("events = %+v, unexpected order/content", events)
	}
	occs := BuildOccurrences([]AttemptReport{report}, KeyClassnameName)
	mt := Merge(occs[0])
	if mt.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want Failed", mt.Outcome)
	}
	if len(mt.Prior) != 1 {
		t.Fatalf("Prior = %d, want 1", len(mt.Prior))
	}
}

// TestJUnitRoundTrip_FlakyScenario is the double-count scenario from the
// README's Why section: a test fails on attempt 1 and passes on attempt 2.
// The merged JUnit output must mark it flaky, not failed, and every plain
// (non-flaky) suite must validate against the fetched JUnit XSD; see
// TestWriteJUnit_ValidatesAgainstXSD in xsd_test.go for the schema check.
func TestJUnitRoundTrip_FlakyScenario(t *testing.T) {
	a1, err := ParseJUnit(readTestdata(t, "junit/surefire.xml"), 1)
	if err != nil {
		t.Fatal(err)
	}
	// Attempt 2: testAdd now passes, testSub unchanged.
	a2data := []byte(`<?xml version="1.0"?>
<testsuite name="com.example.CalcTest" tests="1" failures="0" errors="0" skipped="0" time="0.3">
  <testcase classname="com.example.CalcTest" name="testAdd" time="0.3"/>
</testsuite>`)
	a2, err := ParseJUnit(a2data, 2)
	if err != nil {
		t.Fatal(err)
	}

	merged := MergeAll([]AttemptReport{a1, a2}, KeyClassnameName)
	var got Outcome
	for _, mt := range merged {
		if mt.Identity.Name == "testAdd" {
			got = mt.Outcome
		}
	}
	if got != OutcomeFlaky {
		t.Fatalf("testAdd outcome = %v, want Flaky", got)
	}

	xmlOut, err := WriteJUnit(merged, mustParseTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(xmlOut), `flaky="true"`, "<flakyFailure", "testAdd") {
		t.Fatalf("expected flaky markers in output, got:\n%s", xmlOut)
	}
	if containsAll(string(xmlOut), `<failure`) {
		t.Fatalf("flaky test must not have a top-level <failure>, got:\n%s", xmlOut)
	}
}
