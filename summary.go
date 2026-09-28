package flakemerge

import (
	"fmt"
	"io"
)

// WriteTextSummary writes a human-readable summary of merged tests to w:
// counts by outcome, then one line per flaky or failed test.
func WriteTextSummary(w io.Writer, tests []MergedTest) error {
	s := Summarize(tests)
	if _, err := fmt.Fprintf(w, "%d tests: %d passed, %d flaky, %d failed, %d skipped\n",
		s.Total, s.Passed, s.Flaky, s.Failed, s.Skipped); err != nil {
		return err
	}

	for _, t := range tests {
		if t.Outcome != OutcomeFlaky {
			continue
		}
		if _, err := fmt.Fprintf(w, "FLAKY   %s.%s (failed %d/%d attempts, passed on attempt %d)\n",
			t.Identity.Suite, t.Identity.Name, len(t.Prior), len(t.Attempts), lastAttempt(t)); err != nil {
			return err
		}
	}
	for _, t := range tests {
		if t.Outcome != OutcomeFailed {
			continue
		}
		msg := t.Primary.Message
		if msg == "" {
			msg = t.Primary.Type
		}
		if _, err := fmt.Fprintf(w, "FAILED  %s.%s (%s, %d attempt(s)): %s\n",
			t.Identity.Suite, t.Identity.Name, t.FinalStatus, len(t.Attempts), msg); err != nil {
			return err
		}
	}
	return nil
}

func lastAttempt(t MergedTest) int {
	if len(t.Attempts) == 0 {
		return 0
	}
	return t.Attempts[len(t.Attempts)-1]
}
