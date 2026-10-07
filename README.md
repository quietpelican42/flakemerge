# flakemerge

Merges JUnit XML or CTRF JSON test reports from repeated CI attempts into one
report, with a test that failed then passed on retry counted as flaky, not
failed.

```sh
flakemerge --attempt 1 attempt-1/*.xml --attempt 2 attempt-2/*.xml \
  --out-junit merged.xml --out-ctrf merged.json
```

## Why

A CI job that retries failed tests produces one report per attempt. Naively
concatenating those reports keeps a test's failing entry from attempt 1
alongside its passing entry from attempt 2, so any downstream reader (a
dashboard, a required-check gate, a flaky-test tracker) still sees the test
as failed even though it passed. Merging attempts correctly requires knowing
that a later pass overrides an earlier failure for the *same* test, which
means matching tests by identity across files, not just concatenating them.

Two widely used tools were checked, on the fixtures below (attempt 1: a
suite where `testFlaky` fails and `testStable` passes; attempt 2: the same
suite with only `testFlaky`, now passing):

Measured: `jrm merged.xml attempt1.xml attempt2.xml` (`junit-report-merger`
9.0.4, the top result for "junit merge" on npm)

```xml
<testsuites tests="3" failures="1" errors="0" skipped="0" time="1.5">
  <testsuite name="com.example.FooTest" tests="3" failures="1" errors="0" skipped="0" time="1.5">
    <testcase classname="com.example.FooTest" name="testFlaky" time="0.5">
      <failure message="assertion failed" type="AssertionError">stack trace here</failure>
    </testcase>
    <testcase classname="com.example.FooTest" name="testStable" time="1.0"></testcase>
    <testcase classname="com.example.FooTest" name="testFlaky" time="0.6"></testcase>
  </testsuite>
</testsuites>
```

`testFlaky` appears twice - once failed, once passed - and the suite still
reports `failures="1"`. It is not deduplicated at all; a reader has to
already know to look for duplicate names and reconcile them itself.

Measured: `ctrf merge <dir> --output merged.json` (`ctrf-cli` 0.3.1,
`ctrf-io`'s own reference CLI for the format)

```json
{
  "results": {
    "summary": { "tests": 3, "passed": 2, "failed": 1, "start": 1000, "stop": 2600 },
    "tests": [
      { "name": "testFlaky", "status": "failed", "duration": 500, "suite": "com.example.FooTest" },
      { "name": "testStable", "status": "passed", "duration": 1000, "suite": "com.example.FooTest" },
      { "name": "testFlaky", "status": "passed", "duration": 600, "suite": "com.example.FooTest" }
    ]
  }
}
```

Same result: two `testFlaky` entries, `summary.failed: 1`. Its own `ctrf
flaky <file>` subcommand, run against this merged file, reports "No flaky
tests found" - it looks for tests already marked `flaky: true`, which
`ctrf merge` never sets.

flakemerge on the same two files:

```
2 tests: 1 passed, 1 flaky, 0 failed, 0 skipped
FLAKY   com.example.FooTest.testFlaky (failed 1/2 attempts, passed on attempt 2)
```

with `testFlaky` written out as one entry, `flaky: true`, `retries: 1`, in
both output formats (see Reference below).

Also checked, by reading their documentation rather than running them
(reading `gotestsum`'s and `dorny/test-reporter`'s READMEs, and
`EnricoMi/publish-unit-test-result-action`'s): none of the three documents
merging repeated attempts into flaky-vs-failed semantics. `gotestsum
--rerun-fails` reruns failed tests but its README does not say how a test
that later passes is represented in its output files; `dorny/test-reporter`
and `EnricoMi/publish-unit-test-result-action` parse and report on whatever
files they are given, with no documented retry-deduplication (the latter's
`report_individual_runs` option displays every run rather than reconciling
them). No standalone tool that already does this correctly was found; the
premise that this is unhandled held up.

## Installation

```sh
go install github.com/quietpelican42/flakemerge/cmd/flakemerge@latest
```

Or as a library: `go get github.com/quietpelican42/flakemerge`.

## Usage

### Merge explicit attempts

```sh
flakemerge --attempt 1 ci-run-1/results.xml --attempt 2 ci-run-2/results.xml \
  --out-junit merged.xml --out-ctrf merged.json
```

### Merge attempts given as globs

Each glob is one attempt, in the order given; every file it matches belongs
to that attempt (useful for a sharded attempt that wrote several files):

```sh
flakemerge "attempts/1/*.xml" "attempts/2/*.xml" --out-junit merged.xml
```

### Use as a library

```go
reports := []flakemerge.AttemptReport{report1, report2} // from ParseJUnit/ParseCTRF
merged := flakemerge.MergeAll(reports, flakemerge.KeyClassnameName)
summary := flakemerge.Summarize(merged)
os.Exit(flakemerge.ExitCode(summary))
```

### Choose a test identity

A suite renamed between attempts breaks the default identity. Re-key on name
alone:

```sh
flakemerge --key name --attempt 1 a.xml --attempt 2 b.xml --out-junit merged.xml
```

## Reference

### CLI flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--attempt N file` | repeatable | - | Assign `file` to attempt `N`. Cannot be combined with positional globs. |
| `--key MODE` | `classname`\|`name`\|`classname+file` | `classname` | How tests are matched across attempts. See below. |
| `--out-junit PATH` | string | - | Write merged JUnit XML to `PATH`. Omit to skip. |
| `--out-ctrf PATH` | string | - | Write merged CTRF JSON to `PATH`. Omit to skip. |
| `--out-summary PATH` | string | stdout | Write the text summary to `PATH`. |
| `-h`, `--help` | flag | - | Print usage. |

Input dialect (JUnit XML vs. CTRF JSON) is detected from each file's first
non-whitespace character (`<` or `{`), not its extension.

Exit codes: `0` no test failed (flaky tests do not count as failures), `1`
one or more tests failed on every attempt they ran, `2` bad input - a file
could not be read or parsed, a glob matched nothing, or the arguments were
invalid.

### Test identity (`--key`)

| Mode | Matches on | Use when |
|---|---|---|
| `classname` (default) | suite/classname + test name | The common case: suite names are stable across attempts. |
| `name` | test name only | A suite is renamed or regrouped between attempts but test names are stable. |
| `classname+file` | suite/classname + test name + file | The same class/test name can legitimately appear in more than one file and those must not be merged. |

Within one attempt, duplicate (suite, name) pairs are disambiguated by
position: the Nth case matching a key in one attempt pairs with the Nth case
matching that key in every other attempt.

### Library (package `flakemerge`)

`ParseJUnit(data []byte, attempt int) (AttemptReport, error)` parses one
JUnit XML file (Surefire/Ant, pytest `junit_family=xunit1`/`xunit2`,
jest-junit, go-junit-report, gotestsum all share this shape) tagged with its
attempt number. Also understands Surefire's own `<flakyFailure>`/
`<flakyError>`/`<rerunFailure>`/`<rerunError>` elements nested inside a
`<testcase>`, expanding each into a prior event.

`ParseCTRF(data []byte, attempt int) (AttemptReport, error)` parses one CTRF
JSON file. Also expands an input test's own `retryAttempts` into prior
events, the same way.

`MergeAll(reports []AttemptReport, mode KeyMode) []MergedTest` groups and
merges every attempt's cases into final, per-test outcomes: `OutcomePassed`,
`OutcomeFlaky`, `OutcomeFailed`, or `OutcomeSkipped`. The rule: a test whose
*last* executed (non-skipped) attempt passed is passed overall, flagged
flaky if any earlier attempt failed or errored; a test whose last executed
attempt failed or errored is failed overall, regardless of any earlier pass;
a test skipped in every attempt it appeared in is skipped. `Error` and
`Failure` are tracked as distinct statuses throughout, never collapsed into
each other.

`WriteJUnit(tests []MergedTest, generatedAt time.Time) ([]byte, error)` and
`WriteCTRF(tests []MergedTest, start time.Time) ([]byte, error)` render the
merged result. `WriteTextSummary(w io.Writer, tests []MergedTest) error`
writes the human-readable summary.

`Summarize(tests []MergedTest) Summary` and `ExitCode(s Summary) int` give
the counts and the process exit code.

Timing: a merged test's `time`/`duration` is its last executed attempt's
time; the sum across every attempt is also recorded (`totalTime` property in
JUnit output, implicitly recoverable from `retryAttempts` durations in CTRF
output).

## How it works

Every parsed test case becomes one `Event` (status, message, time, ...)
tagged with its attempt number. Cases from every attempt are grouped by the
chosen key (with positional disambiguation for same-key duplicates within
one attempt) into a `TestOccurrence`: one ordered event history per test.
`Merge` resolves that history to a single outcome using only two facts -
whether the last executed event passed, and whether any earlier executed
event failed or errored - which is exactly what "flaky" means: eventually
green, but not clean. JUnit's `<flakyFailure>` (final: passed) and
`<rerunFailure>` (final: still failing) conventions map directly onto this:
the earlier failing events become nested `<flakyFailure>`/`<rerunFailure>`
elements, and a `flaky="true"` attribute plus a `<properties>` block
(`flaky`, `retries`, `totalTime`) are added for readers that only understand
plain JUnit. CTRF output uses the schema's own `flaky`, `retries`, and
`retryAttempts` fields the same way (see `examples/with-retries.json` in
`ctrf-io/ctrf`, which this package's output matches in shape).

## Limitations

- Only JUnit XML and CTRF JSON are read; no TAP, no native `go test -json`.
- A JUnit `<testcase>` with duplicate identical rerun-family elements
  interleaved in a fabricated order (e.g. hand-edited XML mixing
  `<flakyFailure>` and `<flakyError>` children out of document order) is
  read back grouped by element type, not strict document order; real
  Surefire output never interleaves them, so this does not affect output
  from actual CI runs.
- Flaky/rerun JUnit output (the `flakyFailure`/`rerunFailure` elements, the
  `flaky` attribute, and the nested `<properties>` block) is not valid
  against the generic Ant/JUnit XSD fetched for `TestWriteJUnit_*` in
  `schema_test.go`: that schema predates Surefire's flaky-rerun extension
  and does not model it, and no separate, fetchable XSD for that extension
  was found. Plain (non-flaky) output does validate against it -
  `TestWriteJUnit_ValidatesAgainstXSD` checks this; `TestWriteJUnit_
  FlakyExtendsBeyondXSD` checks and documents the gap instead of hiding it.
- CTRF's `pending` and `other` statuses both map onto this package's
  `Skipped` internally (CTRF has no separate error status either, unlike
  JUnit); round-tripped through CTRF output they come back as `skipped`,
  which loses the original distinction if it mattered.
- `--key` disambiguates duplicate names within one attempt by position
  only, not by any richer identity (e.g. parameters); two tests with the
  same name whose call order differs between attempts pair incorrectly.
- No wall-clock "when did this run" is fabricated: JUnit's per-suite
  `timestamp` output is the earliest attempt's own timestamp when the input
  carried one (JUnit's `timestamp` attribute, or CTRF's `summary.start`),
  otherwise the merge time; CTRF's `start`/`stop` fall back to a zero-based
  synthetic timeline (`start: 0`, `stop: <summed duration>`) under the same
  condition, which is stated here rather than left to look like real wall
  time.

## Compatibility

Built and tested with Go 1.27.1 on darwin/arm64. `go.mod` declares `go
1.23`; not tested against any other Go version. JUnit XSD and CTRF JSON
schema validation in the test suite ran with `xmllint` (libxml2 2.9.13) and
a hand-written draft-07 subset validator (stdlib only, see `schema_test.go`)
against the schema fetched live from GitHub at test time.

## Development

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .   # must print nothing
```

`TestWriteJUnit_ValidatesAgainstXSD`, `TestWriteJUnit_FlakyExtendsBeyondXSD`,
and `TestWriteCTRF_ValidatesAgainstSchema` fetch their reference schema over
the network (pinned to a commit SHA, checked against a pinned sha256) into
`.testdata-cache/` (gitignored) and skip loudly if there is no network
access; the JUnit ones also skip if `xmllint` is not installed.

## License

MIT. See [LICENSE](LICENSE).
