package flakemerge

import "testing"

func ev(status Status, t float64) Event { return Event{Status: status, Time: t} }

func TestMerge_PassAfterFail_IsFlaky(t *testing.T) {
	occ := TestOccurrence{
		Identity: Identity{Suite: "Foo", Name: "testFlaky"},
		Events:   []Event{ev(StatusFailed, 0.5), ev(StatusPassed, 0.6)},
	}
	mt := Merge(occ)
	if mt.Outcome != OutcomeFlaky {
		t.Fatalf("Outcome = %v, want Flaky", mt.Outcome)
	}
	if mt.FinalStatus != StatusPassed {
		t.Fatalf("FinalStatus = %v, want Passed", mt.FinalStatus)
	}
	if len(mt.Prior) != 1 || mt.Prior[0].Status != StatusFailed {
		t.Fatalf("Prior = %+v, want one failed event", mt.Prior)
	}
	if mt.Retries != 1 {
		t.Fatalf("Retries = %d, want 1", mt.Retries)
	}
	if mt.LastTime != 0.6 {
		t.Fatalf("LastTime = %v, want 0.6 (the last attempt)", mt.LastTime)
	}
	if mt.TotalTime != 1.1 {
		t.Fatalf("TotalTime = %v, want 1.1 (sum of both attempts)", mt.TotalTime)
	}
}

func TestMerge_FailedEveryAttempt_IsFailed(t *testing.T) {
	occ := TestOccurrence{
		Identity: Identity{Suite: "Foo", Name: "testAlwaysFails"},
		Events:   []Event{ev(StatusFailed, 0.1), ev(StatusFailed, 0.1), ev(StatusFailed, 0.1)},
	}
	mt := Merge(occ)
	if mt.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want Failed", mt.Outcome)
	}
	if len(mt.Prior) != 2 {
		t.Fatalf("Prior = %d events, want 2 (the two attempts before the final failure)", len(mt.Prior))
	}
	if mt.Retries != 2 {
		t.Fatalf("Retries = %d, want 2", mt.Retries)
	}
}

func TestMerge_FailedThenPassedThenFailed_IsFailed(t *testing.T) {
	// The spec is final-state driven: what matters is whether the LAST
	// attempt passed, not whether some attempt in the middle did.
	occ := TestOccurrence{
		Events: []Event{ev(StatusFailed, 0.1), ev(StatusPassed, 0.1), ev(StatusFailed, 0.1)},
	}
	mt := Merge(occ)
	if mt.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want Failed (last attempt failed)", mt.Outcome)
	}
}

func TestMerge_SkippedEverywhere_IsSkipped(t *testing.T) {
	occ := TestOccurrence{
		Events: []Event{ev(StatusSkipped, 0), ev(StatusSkipped, 0)},
	}
	mt := Merge(occ)
	if mt.Outcome != OutcomeSkipped {
		t.Fatalf("Outcome = %v, want Skipped", mt.Outcome)
	}
}

func TestMerge_SkippedThenRan_IgnoresTheSkip(t *testing.T) {
	// A test skipped on attempt 1 (e.g. quarantined) and run on attempt 2
	// should be judged purely on the attempt(s) that actually ran it.
	occ := TestOccurrence{
		Events: []Event{ev(StatusSkipped, 0), ev(StatusPassed, 0.2)},
	}
	mt := Merge(occ)
	if mt.Outcome != OutcomePassed {
		t.Fatalf("Outcome = %v, want Passed", mt.Outcome)
	}
	if mt.Retries != 0 {
		t.Fatalf("Retries = %d, want 0 (the skip doesn't count as an attempt)", mt.Retries)
	}
}

func TestMerge_ErrorVsFailurePreserved(t *testing.T) {
	occ := TestOccurrence{
		Events: []Event{ev(StatusError, 0.1)},
	}
	mt := Merge(occ)
	if mt.FinalStatus != StatusError {
		t.Fatalf("FinalStatus = %v, want Error (not collapsed into Failed)", mt.FinalStatus)
	}
	if mt.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want Failed", mt.Outcome)
	}
}

func TestMerge_CleanPass_NotFlagged(t *testing.T) {
	occ := TestOccurrence{Events: []Event{ev(StatusPassed, 0.1)}}
	mt := Merge(occ)
	if mt.Outcome != OutcomePassed {
		t.Fatalf("Outcome = %v, want Passed", mt.Outcome)
	}
	if len(mt.Prior) != 0 {
		t.Fatalf("Prior = %+v, want none", mt.Prior)
	}
}
