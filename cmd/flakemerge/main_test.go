package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const passingXML = `<?xml version="1.0"?>
<testsuite name="pkg.T" tests="1" failures="0" errors="0" skipped="0" time="0.1">
  <testcase classname="pkg.T" name="testOne" time="0.1"/>
</testsuite>`

const failingXML = `<?xml version="1.0"?>
<testsuite name="pkg.T" tests="1" failures="1" errors="0" skipped="0" time="0.1">
  <testcase classname="pkg.T" name="testOne" time="0.1">
    <failure message="nope" type="AssertionError">trace</failure>
  </testcase>
</testsuite>`

func TestRun_ExitCodeZero_NoFailures(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "a.xml", passingXML)
	got := run([]string{"--attempt", "1", f, "--out-summary", filepath.Join(dir, "summary.txt")})
	if got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
}

func TestRun_ExitCodeOne_HardFailure(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "a.xml", failingXML)
	got := run([]string{"--attempt", "1", f, "--out-summary", filepath.Join(dir, "summary.txt")})
	if got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
}

func TestRun_ExitCodeZero_FlakyIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	f1 := writeFile(t, dir, "a1.xml", failingXML)
	f2 := writeFile(t, dir, "a2.xml", passingXML)
	got := run([]string{"--attempt", "1", f1, "--attempt", "2", f2, "--out-summary", filepath.Join(dir, "summary.txt")})
	if got != 0 {
		t.Fatalf("exit code = %d, want 0 (flaky, not failed)", got)
	}
}

func TestRun_ExitCodeTwo_MalformedInput(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "bad.xml", "<testsuite><testcase>")
	got := run([]string{"--attempt", "1", f, "--out-summary", filepath.Join(dir, "summary.txt")})
	if got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestRun_ExitCodeTwo_NoInput(t *testing.T) {
	got := run(nil)
	if got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestRun_ExitCodeTwo_GlobMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	got := run([]string{filepath.Join(dir, "no-such-*.xml")})
	if got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestRun_PositionalGlobs_OneAttemptPerGlob(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeFile(t, dir1, "a.xml", failingXML)
	writeFile(t, dir2, "a.xml", passingXML)
	got := run([]string{filepath.Join(dir1, "*.xml"), filepath.Join(dir2, "*.xml"), "--out-summary", filepath.Join(dir1, "summary.txt")})
	if got != 0 {
		t.Fatalf("exit code = %d, want 0 (glob 1 = attempt 1 fails, glob 2 = attempt 2 passes -> flaky)", got)
	}
}

func TestRun_WritesJUnitAndCTRFOutputs(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "a.xml", passingXML)
	junitOut := filepath.Join(dir, "out.xml")
	ctrfOut := filepath.Join(dir, "out.json")
	got := run([]string{"--attempt", "1", f, "--out-junit", junitOut, "--out-ctrf", ctrfOut, "--out-summary", filepath.Join(dir, "s.txt")})
	if got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if _, err := os.Stat(junitOut); err != nil {
		t.Errorf("JUnit output not written: %v", err)
	}
	if _, err := os.Stat(ctrfOut); err != nil {
		t.Errorf("CTRF output not written: %v", err)
	}
}
