package flakemerge

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The JUnit dialects this package reads (Surefire/Ant, pytest
// junit_family=xunit1/xunit2, jest-junit, go-junit-report, gotestsum) all
// share this shape: an optional <testsuites> wrapper around one or more
// <testsuite>, each holding <testcase> elements with a <failure>/<error>/
// <skipped> child. They differ only in which optional attributes they set
// (classname vs name-only, file, timestamp) and in whether the wrapper is
// present, all of which this reader tolerates.

type xmlTestSuites struct {
	XMLName xml.Name       `xml:"testsuites"`
	Suites  []xmlTestSuite `xml:"testsuite"`
}

type xmlTestSuite struct {
	Name      string        `xml:"name,attr"`
	Timestamp string        `xml:"timestamp,attr"`
	Cases     []xmlTestCase `xml:"testcase"`
}

type xmlTestCase struct {
	Name          string      `xml:"name,attr"`
	Classname     string      `xml:"classname,attr"`
	File          string      `xml:"file,attr"`
	Time          string      `xml:"time,attr"`
	Failure       *xmlFailure `xml:"failure"`
	Error         *xmlFailure `xml:"error"`
	Skipped       *xmlFailure `xml:"skipped"`
	SystemOut     string      `xml:"system-out"`
	SystemErr     string      `xml:"system-err"`
	FlakyFailures []xmlRerun  `xml:"flakyFailure"`
	FlakyErrors   []xmlRerun  `xml:"flakyError"`
	RerunFailures []xmlRerun  `xml:"rerunFailure"`
	RerunErrors   []xmlRerun  `xml:"rerunError"`
}

type xmlFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// xmlRerun models Surefire's nested <flakyFailure>/<flakyError>/
// <rerunFailure>/<rerunError> elements: message/type attributes, plus either
// a <stackTrace> child (Surefire's own convention) or raw character data
// (what other producers of this extension tend to emit).
type xmlRerun struct {
	Message    string `xml:"message,attr"`
	Type       string `xml:"type,attr"`
	StackTrace string `xml:"stackTrace"`
	SystemOut  string `xml:"system-out"`
	SystemErr  string `xml:"system-err"`
	Body       string `xml:",chardata"`
}

func (r xmlRerun) body() string {
	if r.StackTrace != "" {
		return r.StackTrace
	}
	return strings.TrimSpace(r.Body)
}

// sniffRootElement returns the local name of the document's root element,
// without validating the rest of the document.
func sniffRootElement(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("no root element found")
			}
			return "", err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}

// ParseJUnit parses one JUnit XML attempt file (either a lone <testsuite> or
// a <testsuites> wrapper) into an AttemptReport. It returns an error for
// malformed XML or an unrecognized root element; callers should treat that
// as bad input (exit code 2).
func ParseJUnit(data []byte, attempt int) (AttemptReport, error) {
	root, err := sniffRootElement(data)
	if err != nil {
		return AttemptReport{}, fmt.Errorf("parse JUnit XML: %w", err)
	}

	var suites []xmlTestSuite
	switch root {
	case "testsuites":
		var v xmlTestSuites
		if err := xml.Unmarshal(data, &v); err != nil {
			return AttemptReport{}, fmt.Errorf("parse JUnit XML: %w", err)
		}
		suites = v.Suites
	case "testsuite":
		var v xmlTestSuite
		if err := xml.Unmarshal(data, &v); err != nil {
			return AttemptReport{}, fmt.Errorf("parse JUnit XML: %w", err)
		}
		suites = []xmlTestSuite{v}
	default:
		return AttemptReport{}, fmt.Errorf("parse JUnit XML: unrecognized root element <%s>", root)
	}

	report := AttemptReport{Attempt: attempt}
	for _, suite := range suites {
		if report.Timestamp == nil {
			if t, ok := parseJUnitTimestamp(suite.Timestamp); ok {
				report.Timestamp = &t
			}
		}
		for _, tc := range suite.Cases {
			report.Cases = append(report.Cases, junitCaseToParsedCase(suite, tc))
		}
	}
	return report, nil
}

func parseJUnitTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func junitCaseToParsedCase(suite xmlTestSuite, tc xmlTestCase) ParsedCase {
	suiteName := tc.Classname
	if suiteName == "" {
		suiteName = suite.Name
	}
	id := Identity{Suite: suiteName, Name: tc.Name, File: tc.File}

	dur, _ := strconv.ParseFloat(strings.TrimSpace(tc.Time), 64)

	var prior []Event
	appendRerun := func(rs []xmlRerun, status Status) {
		for _, r := range rs {
			prior = append(prior, Event{
				Status:    status,
				Type:      r.Type,
				Message:   r.Message,
				Body:      r.body(),
				SystemOut: r.SystemOut,
				SystemErr: r.SystemErr,
			})
		}
	}
	appendRerun(tc.FlakyFailures, StatusFailed)
	appendRerun(tc.FlakyErrors, StatusError)
	appendRerun(tc.RerunFailures, StatusFailed)
	appendRerun(tc.RerunErrors, StatusError)

	final := Event{
		Time:      dur,
		SystemOut: tc.SystemOut,
		SystemErr: tc.SystemErr,
	}
	switch {
	case tc.Skipped != nil:
		final.Status = StatusSkipped
		final.Message = tc.Skipped.Message
		final.Body = strings.TrimSpace(tc.Skipped.Body)
	case tc.Error != nil:
		final.Status = StatusError
		final.Type = tc.Error.Type
		final.Message = tc.Error.Message
		final.Body = strings.TrimSpace(tc.Error.Body)
	case tc.Failure != nil:
		final.Status = StatusFailed
		final.Type = tc.Failure.Type
		final.Message = tc.Failure.Message
		final.Body = strings.TrimSpace(tc.Failure.Body)
	default:
		final.Status = StatusPassed
	}

	events := append(prior, final)
	return ParsedCase{Identity: id, Events: events}
}

// --- Writer -----------------------------------------------------------

// WriteJUnit renders merged tests as JUnit XML, grouped into <testsuite>
// elements by Identity.Suite. A flaky test (passed overall, failed on an
// earlier attempt) is written with no <failure>/<error> of its own, per
// Surefire's flakyFailure/flakyError convention, plus a `flaky="true"`
// attribute and a nested <properties> block (flaky, retries, totalTime) for
// tools that do not know that convention. A test that failed on every
// attempt keeps its <failure>/<error> and additionally records earlier
// attempts as <rerunFailure>/<rerunError>, per the same Surefire convention.
//
// generatedAt fills each <testsuite>'s required timestamp attribute. A
// single output testsuite spans several input attempts that each ran at a
// different time, so there is no one honest "when this ran"; generatedAt
// should be the earliest attempt's timestamp when the caller has one, or
// the merge time otherwise, and either way is a merge-time artifact, not a
// per-test measurement.
func WriteJUnit(tests []MergedTest, generatedAt time.Time) ([]byte, error) {
	type suiteAcc struct {
		name             string
		order            int
		cases            []MergedTest
		tests, fail, err int
		skip             int
		time             float64
	}
	suites := map[string]*suiteAcc{}
	var suiteOrder []string
	for _, t := range tests {
		name := t.Identity.Suite
		acc, ok := suites[name]
		if !ok {
			acc = &suiteAcc{name: name}
			suites[name] = acc
			suiteOrder = append(suiteOrder, name)
		}
		acc.cases = append(acc.cases, t)
		acc.tests++
		acc.time += t.LastTime
		switch t.Outcome {
		case OutcomeFailed:
			if t.FinalStatus == StatusError {
				acc.err++
			} else {
				acc.fail++
			}
		case OutcomeSkipped:
			acc.skip++
		}
	}

	timestamp := generatedAt.UTC().Format("2006-01-02T15:04:05")

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString("<testsuites>\n")
	for i, name := range suiteOrder {
		acc := suites[name]
		fmt.Fprintf(&buf, "  <testsuite name=%s tests=\"%d\" failures=\"%d\" errors=\"%d\" skipped=\"%d\" time=%s package=%s id=\"%d\" timestamp=%s hostname=\"localhost\">\n",
			xmlAttr(acc.name), acc.tests, acc.fail, acc.err, acc.skip, xmlAttr(formatTime(acc.time)),
			xmlAttr(acc.name), i, xmlAttr(timestamp))
		buf.WriteString("    <properties/>\n")
		for _, t := range acc.cases {
			writeJUnitCase(&buf, t)
		}
		buf.WriteString("    <system-out></system-out>\n")
		buf.WriteString("    <system-err></system-err>\n")
		buf.WriteString("  </testsuite>\n")
	}
	buf.WriteString("</testsuites>\n")
	return buf.Bytes(), nil
}

func writeJUnitCase(buf *bytes.Buffer, t MergedTest) {
	flakyAttr := ""
	if t.Outcome == OutcomeFlaky {
		flakyAttr = ` flaky="true"`
	}
	fmt.Fprintf(buf, "    <testcase name=%s classname=%s time=%s%s>\n",
		xmlAttr(t.Identity.Name), xmlAttr(t.Identity.Suite), xmlAttr(formatTime(t.LastTime)), flakyAttr)

	switch t.Outcome {
	case OutcomeSkipped:
		fmt.Fprintf(buf, "      <skipped%s/>\n", messageAttr(t.Primary.Message))
	case OutcomeFailed:
		writeJUnitFailureLike(buf, elementFor(t.FinalStatus), t.Primary)
		for _, e := range t.Prior {
			writeJUnitRerun(buf, rerunElementFor(e.Status), e)
		}
	case OutcomeFlaky:
		for _, e := range t.Prior {
			writeJUnitRerun(buf, flakyElementFor(e.Status), e)
		}
	}

	if t.Outcome == OutcomeFlaky || t.Retries > 0 {
		buf.WriteString("      <properties>\n")
		if t.Outcome == OutcomeFlaky {
			buf.WriteString("        <property name=\"flaky\" value=\"true\"/>\n")
		}
		fmt.Fprintf(buf, "        <property name=\"retries\" value=\"%d\"/>\n", t.Retries)
		fmt.Fprintf(buf, "        <property name=\"totalTime\" value=%s/>\n", xmlAttr(formatTime(t.TotalTime)))
		buf.WriteString("      </properties>\n")
	}

	buf.WriteString("    </testcase>\n")
}

func elementFor(s Status) string {
	if s == StatusError {
		return "error"
	}
	return "failure"
}
func rerunElementFor(s Status) string {
	if s == StatusError {
		return "rerunError"
	}
	return "rerunFailure"
}
func flakyElementFor(s Status) string {
	if s == StatusError {
		return "flakyError"
	}
	return "flakyFailure"
}

func writeJUnitFailureLike(buf *bytes.Buffer, elem string, e Event) {
	fmt.Fprintf(buf, "      <%s%s%s>%s</%s>\n", elem, messageAttr(e.Message), typeAttr(e.Type), xmlEscapeText(e.Body), elem)
}

func writeJUnitRerun(buf *bytes.Buffer, elem string, e Event) {
	fmt.Fprintf(buf, "      <%s%s%s>\n        <stackTrace>%s</stackTrace>\n      </%s>\n",
		elem, messageAttr(e.Message), typeAttr(e.Type), xmlEscapeText(e.Body), elem)
}

func messageAttr(s string) string {
	if s == "" {
		return ""
	}
	return " message=" + xmlAttr(s)
}
func typeAttr(s string) string {
	if s == "" {
		return ""
	}
	return " type=" + xmlAttr(s)
}

func xmlAttr(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return `"` + buf.String() + `"`
}

func xmlEscapeText(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func formatTime(f float64) string {
	return strconv.FormatFloat(f, 'f', 3, 64)
}
