package flakemerge

import (
	"sort"
	"time"
)

// ParsedCase is one test case occurrence as read from a single input file
// (one attempt), before merging across attempts.
type ParsedCase struct {
	Identity Identity
	// Events holds the sub-events for this occurrence in document order:
	// normally exactly one (the testcase's own result). A JUnit file that
	// already embeds Surefire's nested flakyFailure/flakyError/
	// rerunFailure/rerunError elements yields more than one, oldest first,
	// ending with the outer testcase's own result. Attempt/SubOrder are
	// filled in by BuildOccurrences.
	Events []Event
}

// AttemptReport is one input file's parsed content, tagged with its attempt
// number.
type AttemptReport struct {
	Attempt int
	Cases   []ParsedCase
	// Timestamp is the earliest wall-clock time this attempt's source
	// reported (JUnit testsuite timestamp, or a CTRF summary.start), when
	// the dialect records one. It is used only to give CTRF output a real
	// start/stop timeline instead of a synthetic one; nil if unavailable.
	Timestamp *time.Time
}

// BuildOccurrences groups parsed cases from every attempt into per-test
// occurrences, ready for Merge. Reports are processed in ascending Attempt
// order regardless of the order they were passed in, so --attempt flags (or
// globs) may be given out of order. Duplicate test names within a single
// attempt are disambiguated by position: the Nth case matching a given key
// in one attempt is paired with the Nth case matching that key in every
// other attempt. Output order follows first appearance across attempts.
func BuildOccurrences(reports []AttemptReport, mode KeyMode) []TestOccurrence {
	sorted := make([]AttemptReport, len(reports))
	copy(sorted, reports)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Attempt < sorted[j].Attempt })

	type occKey struct {
		key string
		ord int
	}
	occs := make(map[occKey]*TestOccurrence)
	var order []occKey

	for _, report := range sorted {
		seen := make(map[string]int) // key -> count so far in this attempt
		for _, pc := range report.Cases {
			k := pc.Identity.key(mode)
			ord := seen[k]
			seen[k] = ord + 1
			ok := occKey{k, ord}

			events := make([]Event, len(pc.Events))
			copy(events, pc.Events)
			for i := range events {
				events[i].Attempt = report.Attempt
				events[i].SubOrder = i
			}

			o, exists := occs[ok]
			if !exists {
				o = &TestOccurrence{Key: k, FirstSeen: pc.Identity}
				occs[ok] = o
				order = append(order, ok)
			}
			o.Identity = pc.Identity // last-seen wins for display
			o.Events = append(o.Events, events...)
		}
	}

	result := make([]TestOccurrence, 0, len(order))
	for _, ok := range order {
		o := occs[ok]
		sort.SliceStable(o.Events, func(i, j int) bool {
			if o.Events[i].Attempt != o.Events[j].Attempt {
				return o.Events[i].Attempt < o.Events[j].Attempt
			}
			return o.Events[i].SubOrder < o.Events[j].SubOrder
		})
		result = append(result, *o)
	}
	return result
}

// Summary counts merged tests by outcome.
type Summary struct {
	Total, Passed, Flaky, Failed, Skipped int
}

// Summarize tallies outcomes across merged tests.
func Summarize(tests []MergedTest) Summary {
	var s Summary
	s.Total = len(tests)
	for _, t := range tests {
		switch t.Outcome {
		case OutcomePassed:
			s.Passed++
		case OutcomeFlaky:
			s.Flaky++
		case OutcomeFailed:
			s.Failed++
		case OutcomeSkipped:
			s.Skipped++
		}
	}
	return s
}

// ExitCode returns the process exit code for a summary: 0 if nothing failed,
// 1 if any test failed. Bad input (malformed reports) is signalled
// separately with exit code 2 by the CLI, before a Summary ever exists.
func ExitCode(s Summary) int {
	if s.Failed > 0 {
		return 1
	}
	return 0
}

// MergeAll builds occurrences from every attempt report and merges each one.
// Output order matches first appearance across the (attempt-sorted) inputs.
func MergeAll(reports []AttemptReport, mode KeyMode) []MergedTest {
	occs := BuildOccurrences(reports, mode)
	tests := make([]MergedTest, 0, len(occs))
	for _, occ := range occs {
		tests = append(tests, Merge(occ))
	}
	return tests
}
