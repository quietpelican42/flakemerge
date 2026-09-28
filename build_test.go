package flakemerge

import "testing"

func pc(suite, name string, status Status) ParsedCase {
	return ParsedCase{Identity: Identity{Suite: suite, Name: name}, Events: []Event{{Status: status}}}
}

func TestBuildOccurrences_OnlyInSomeAttempts(t *testing.T) {
	reports := []AttemptReport{
		{Attempt: 1, Cases: []ParsedCase{pc("S", "onlyAttempt1", StatusFailed), pc("S", "both", StatusFailed)}},
		{Attempt: 2, Cases: []ParsedCase{pc("S", "both", StatusPassed), pc("S", "onlyAttempt2", StatusPassed)}},
	}
	occs := BuildOccurrences(reports, KeyClassnameName)
	byName := map[string]TestOccurrence{}
	for _, o := range occs {
		byName[o.Identity.Name] = o
	}
	if len(occs) != 3 {
		t.Fatalf("got %d occurrences, want 3", len(occs))
	}
	if len(byName["onlyAttempt1"].Events) != 1 {
		t.Fatalf("onlyAttempt1: %d events, want 1", len(byName["onlyAttempt1"].Events))
	}
	if len(byName["both"].Events) != 2 {
		t.Fatalf("both: %d events, want 2", len(byName["both"].Events))
	}
	mt := Merge(byName["onlyAttempt1"])
	if mt.Outcome != OutcomeFailed {
		t.Fatalf("onlyAttempt1 outcome = %v, want Failed (it never got a passing retry)", mt.Outcome)
	}
	mt2 := Merge(byName["both"])
	if mt2.Outcome != OutcomeFlaky {
		t.Fatalf("both outcome = %v, want Flaky", mt2.Outcome)
	}
}

func TestBuildOccurrences_RenamedSuite(t *testing.T) {
	// Same test, but its enclosing suite/classname is renamed between
	// attempts. Under the default key (classname+name) that makes it look
	// like two unrelated single-attempt tests; under KeyName it merges.
	reports := []AttemptReport{
		{Attempt: 1, Cases: []ParsedCase{pc("OldSuite", "testX", StatusFailed)}},
		{Attempt: 2, Cases: []ParsedCase{pc("NewSuite", "testX", StatusPassed)}},
	}

	def := BuildOccurrences(reports, KeyClassnameName)
	if len(def) != 2 {
		t.Fatalf("classname mode: got %d occurrences, want 2 (suite rename splits identity)", len(def))
	}
	for _, o := range def {
		if len(o.Events) != 1 {
			t.Fatalf("classname mode: occurrence %+v has %d events, want 1", o.Identity, len(o.Events))
		}
	}

	byName := BuildOccurrences(reports, KeyName)
	if len(byName) != 1 {
		t.Fatalf("name mode: got %d occurrences, want 1 (rename ignored)", len(byName))
	}
	mt := Merge(byName[0])
	if mt.Outcome != OutcomeFlaky {
		t.Fatalf("name mode: outcome = %v, want Flaky", mt.Outcome)
	}
}

func TestBuildOccurrences_DuplicateNamesWithinAnAttempt(t *testing.T) {
	// Two testcases sharing a name in the same attempt (e.g. an
	// unparameterised @Test run twice, or a broken suite). They are paired
	// positionally with the same attempt's duplicates in later attempts.
	reports := []AttemptReport{
		{Attempt: 1, Cases: []ParsedCase{
			pc("S", "dup", StatusFailed), // 1st occurrence, ordinal 0
			pc("S", "dup", StatusPassed), // 2nd occurrence, ordinal 1
		}},
		{Attempt: 2, Cases: []ParsedCase{
			pc("S", "dup", StatusPassed), // pairs with ordinal 0 -> flaky
		}},
	}
	occs := BuildOccurrences(reports, KeyClassnameName)
	if len(occs) != 2 {
		t.Fatalf("got %d occurrences, want 2 (ordinal 0 and ordinal 1 of \"dup\")", len(occs))
	}
	m0 := Merge(occs[0])
	if m0.Outcome != OutcomeFlaky {
		t.Fatalf("ordinal 0: outcome = %v, want Flaky (failed attempt 1, passed attempt 2)", m0.Outcome)
	}
	m1 := Merge(occs[1])
	if m1.Outcome != OutcomePassed || len(m1.Attempts) != 1 {
		t.Fatalf("ordinal 1: outcome = %v attempts=%v, want Passed with a single attempt (only ran once)", m1.Outcome, m1.Attempts)
	}
}

func TestBuildOccurrences_AttemptsSortedRegardlessOfInputOrder(t *testing.T) {
	// Attempt 2's file is passed to BuildOccurrences before attempt 1's.
	reports := []AttemptReport{
		{Attempt: 2, Cases: []ParsedCase{pc("S", "t", StatusPassed)}},
		{Attempt: 1, Cases: []ParsedCase{pc("S", "t", StatusFailed)}},
	}
	occs := BuildOccurrences(reports, KeyClassnameName)
	if len(occs) != 1 {
		t.Fatalf("got %d occurrences, want 1", len(occs))
	}
	events := occs[0].Events
	if len(events) != 2 || events[0].Status != StatusFailed || events[0].Attempt != 1 || events[1].Attempt != 2 {
		t.Fatalf("events = %+v, want attempt 1 (failed) then attempt 2 (passed)", events)
	}
}

func TestKeyClassnameNameFile_DistinguishesSameNameDifferentFile(t *testing.T) {
	a := Identity{Suite: "S", Name: "t", File: "a.go"}
	b := Identity{Suite: "S", Name: "t", File: "b.go"}
	if a.key(KeyClassnameNameFile) == b.key(KeyClassnameNameFile) {
		t.Fatal("expected different keys for the same suite+name in different files")
	}
	if a.key(KeyClassnameName) != b.key(KeyClassnameName) {
		t.Fatal("expected the same key under classname mode, which ignores file")
	}
}
