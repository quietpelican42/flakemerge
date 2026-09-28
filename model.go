// Package flakemerge merges JUnit XML and CTRF JSON test reports produced by
// repeated CI attempts (retries of the same test run) into a single report
// with correct flaky-versus-failed semantics: a test that failed on one
// attempt and passed on a later attempt is flaky (the run counts it as
// passing, but flags it), not failed.
package flakemerge

import "fmt"

// Status is the outcome of a single test execution.
type Status int

// The possible outcomes of a single test execution, ordered so that a plain
// integer comparison never carries meaning — always compare by name.
const (
	StatusPassed Status = iota
	StatusFailed
	StatusError
	StatusSkipped
)

// String returns the lower-case name of s, matching JUnit terminology
// (failure/error/skipped) rather than CTRF's collapsed vocabulary.
func (s Status) String() string {
	switch s {
	case StatusPassed:
		return "passed"
	case StatusFailed:
		return "failed"
	case StatusError:
		return "error"
	case StatusSkipped:
		return "skipped"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// KeyMode selects which fields of an Identity are used to decide whether two
// test cases from different attempts are the same test.
type KeyMode int

const (
	// KeyClassnameName matches on (suite/classname, test name). This is the
	// default: it is what every dialect in this package's scope populates,
	// and it tolerates a test file moving without losing history.
	KeyClassnameName KeyMode = iota
	// KeyName matches on test name only, ignoring the suite/classname. Use
	// this when a suite is renamed between attempts (a rerun that regroups
	// or relabels suites) but test names are stable.
	KeyName
	// KeyClassnameNameFile matches on (suite/classname, test name, file).
	// Use this when the same class/test name can legitimately appear in more
	// than one file and those must not be merged together.
	KeyClassnameNameFile
)

// ParseKeyMode parses a --key flag value. It accepts "classname" (default),
// "name", and "classname+file".
func ParseKeyMode(s string) (KeyMode, error) {
	switch s {
	case "", "classname":
		return KeyClassnameName, nil
	case "name":
		return KeyName, nil
	case "classname+file":
		return KeyClassnameNameFile, nil
	default:
		return 0, fmt.Errorf("unknown key mode %q (want classname, name, or classname+file)", s)
	}
}

// Identity is the raw, per-occurrence data used to decide whether two test
// cases across attempts refer to the same test.
type Identity struct {
	Suite string // testsuite name / classname (JUnit) or "suite" (CTRF)
	Name  string // test / testcase name
	File  string // source file, when the dialect records one; may be empty
}

// key computes the merge key for this identity under the given mode.
func (id Identity) key(mode KeyMode) string {
	switch mode {
	case KeyName:
		return id.Name
	case KeyClassnameNameFile:
		return id.Suite + "\x00" + id.Name + "\x00" + id.File
	default: // KeyClassnameName
		return id.Suite + "\x00" + id.Name
	}
}

// Event is one recorded execution of a test: either a whole CI attempt (an
// entire input file), or one of the nested rerun/flaky elements a dialect
// already embedded inside a single attempt's file. Events for the same test
// are ordered first by Attempt, then by sub-position within that attempt.
type Event struct {
	Attempt   int // 1-based attempt number this event belongs to
	SubOrder  int // order within the attempt (0 = the attempt's own final result)
	Status    Status
	Type      string // exception/assertion class, when known
	Message   string
	Body      string // stack trace / details text
	SystemOut string
	SystemErr string
	Time      float64 // seconds; 0 if unknown
}

// TestOccurrence is one identity's ordered history of Events, plus the
// original identities seen (so output can report the last-seen suite/file
// even when the key mode ignores them).
type TestOccurrence struct {
	Key       string
	Identity  Identity // identity from the most recent event
	FirstSeen Identity // identity from the first event (for diagnostics)
	Events    []Event  // sorted by (Attempt, SubOrder)
}

// Outcome is the resolved, merged verdict for one test across every attempt
// it appeared in.
type Outcome int

const (
	OutcomePassed Outcome = iota
	OutcomeFlaky          // passed, but failed/errored on an earlier attempt
	OutcomeFailed
	OutcomeSkipped
)

func (o Outcome) String() string {
	switch o {
	case OutcomePassed:
		return "passed"
	case OutcomeFlaky:
		return "flaky"
	case OutcomeFailed:
		return "failed"
	case OutcomeSkipped:
		return "skipped"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// MergedTest is one test's final, merged result.
type MergedTest struct {
	Identity Identity
	Outcome  Outcome

	// FinalStatus is the status of the representative (last non-skipped, or
	// last if all were skipped) event: Passed, Failed, Error, or Skipped.
	FinalStatus Status

	// Primary is the event that explains FinalStatus: for Passed/Flaky it is
	// empty (Message/Body unset); for Failed/Error it is the last failing
	// event; for Skipped it is the last skip event.
	Primary Event

	// Prior holds every earlier failing/erroring event, oldest first: for a
	// Flaky test these are the attempts that failed before the eventual
	// pass (-> Surefire flakyFailure/flakyError); for a Failed test these
	// are the attempts that failed before the final, still-failing attempt
	// (-> Surefire rerunFailure/rerunError).
	Prior []Event

	// Retries is the number of executed (non-skipped) attempts beyond the
	// first, i.e. len(non-skip events)-1.
	Retries int

	// LastTime is the duration of the representative event (last attempt).
	LastTime float64
	// TotalTime sums Time across every event (all attempts, all sub-events).
	TotalTime float64

	// Attempts records the attempt numbers this test executed in (skips
	// included), for diagnostics.
	Attempts []int
}

// Merge resolves one TestOccurrence's event history into a MergedTest.
func Merge(occ TestOccurrence) MergedTest {
	mt := MergedTest{Identity: occ.Identity}
	for _, e := range occ.Events {
		mt.Attempts = append(mt.Attempts, e.Attempt)
		mt.TotalTime += e.Time
	}

	var nonSkip []Event
	for _, e := range occ.Events {
		if e.Status != StatusSkipped {
			nonSkip = append(nonSkip, e)
		}
	}

	if len(nonSkip) == 0 {
		// Skipped in every attempt it appeared in.
		last := occ.Events[len(occ.Events)-1]
		mt.Outcome = OutcomeSkipped
		mt.FinalStatus = StatusSkipped
		mt.Primary = last
		mt.LastTime = last.Time
		return mt
	}

	last := nonSkip[len(nonSkip)-1]
	mt.LastTime = last.Time
	mt.Retries = len(nonSkip) - 1

	var priorFailing []Event
	for _, e := range nonSkip[:len(nonSkip)-1] {
		if e.Status == StatusFailed || e.Status == StatusError {
			priorFailing = append(priorFailing, e)
		}
	}

	switch last.Status {
	case StatusPassed:
		mt.FinalStatus = StatusPassed
		if len(priorFailing) > 0 {
			mt.Outcome = OutcomeFlaky
			mt.Prior = priorFailing
		} else {
			mt.Outcome = OutcomePassed
		}
	default: // Failed or Error: failed overall regardless of any earlier pass.
		mt.Outcome = OutcomeFailed
		mt.FinalStatus = last.Status
		mt.Primary = last
		mt.Prior = priorFailing
	}
	return mt
}
