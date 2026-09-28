package flakemerge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// This file validates this package's output against two schemas fetched
// from upstream, pinned to a commit SHA with a sha256 (never committed to
// the repo; cached under .testdata-cache/, which is gitignored). Per this
// batch's third-party rule, both tests skip loudly - not fail - if the
// network or a required external tool is unavailable.

const (
	// Pinned to windyroad/JUnit-Schema commit cfa434d4b8e102a8f55b8727b552a0063ee9044e.
	junitXSDURL    = "https://raw.githubusercontent.com/windyroad/JUnit-Schema/cfa434d4b8e102a8f55b8727b552a0063ee9044e/JUnit.xsd"
	junitXSDSHA256 = "147ff89ef34a82f9a1b2951e7cad251b98c9a7a48a0e0c8e44937141791f57a8"

	// Pinned to ctrf-io/ctrf commit 66e823ca2c9e1f54bf8b387adda0b7e0c7610538.
	ctrfSchemaURL    = "https://raw.githubusercontent.com/ctrf-io/ctrf/66e823ca2c9e1f54bf8b387adda0b7e0c7610538/schema/ctrf.schema.json"
	ctrfSchemaSHA256 = "742dee4cffda469902a9c30c4a87b0b7000d59d2ee502dcccb12ec471cea96a2"
)

func fetchPinned(t *testing.T, url, wantSHA256 string) []byte {
	t.Helper()
	cacheDir := filepath.Join(".testdata-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Skipf("cannot create cache dir: %v", err)
	}
	cachePath := filepath.Join(cacheDir, wantSHA256+".cache")
	if data, err := os.ReadFile(cachePath); err == nil {
		return data
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Skipf("SKIP: no network access to fetch %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("SKIP: fetching %s returned %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skipf("SKIP: reading %s: %v", url, err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != wantSHA256 {
		t.Fatalf("%s: sha256 mismatch: got %s, want %s (pinned content changed upstream)", url, got, wantSHA256)
	}
	_ = os.WriteFile(cachePath, data, 0o644)
	return data
}

// TestWriteJUnit_ValidatesAgainstXSD checks a plain (non-flaky) merged
// report against the fetched Ant/JUnit XSD with xmllint. A flaky or
// still-failing-after-retries testcase uses Surefire's flakyFailure/
// rerunFailure elements and a "flaky"/"properties" extension this generic
// schema does not define - see TestWriteJUnit_FlakyExtendsBeyondXSD, which
// documents that gap instead of hiding it.
func TestWriteJUnit_ValidatesAgainstXSD(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("SKIP: xmllint not found in PATH")
	}
	xsd := fetchPinned(t, junitXSDURL, junitXSDSHA256)

	tests := []MergedTest{
		{Identity: Identity{Suite: "pkg.PlainTest", Name: "testOne"}, Outcome: OutcomePassed, FinalStatus: StatusPassed, LastTime: 0.1},
		{Identity: Identity{Suite: "pkg.PlainTest", Name: "testTwo"}, Outcome: OutcomeFailed, FinalStatus: StatusFailed,
			Primary: Event{Status: StatusFailed, Message: "boom", Type: "AssertionError", Body: "trace"}, LastTime: 0.2},
	}
	out, err := WriteJUnit(tests, mustParseTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	xsdPath := filepath.Join(dir, "junit.xsd")
	xmlPath := filepath.Join(dir, "out.xml")
	if err := os.WriteFile(xsdPath, xsd, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xmlPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("xmllint", "--noout", "--schema", xsdPath, xmlPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xmllint rejected plain merged JUnit output: %v\n%s\ninput:\n%s", err, output, out)
	}
}

// TestWriteJUnit_FlakyExtendsBeyondXSD documents, rather than hides, that a
// flaky/rerun testcase is NOT valid against the generic Ant/JUnit XSD: that
// schema predates and does not model Surefire's flakyFailure/rerunFailure
// elements at all, and testcase does not allow a nested <properties>. No
// formally published XSD for the Surefire extension was found (see README
// Limitations).
func TestWriteJUnit_FlakyExtendsBeyondXSD(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("SKIP: xmllint not found in PATH")
	}
	xsd := fetchPinned(t, junitXSDURL, junitXSDSHA256)

	tests := []MergedTest{
		{Identity: Identity{Suite: "pkg.FlakyTest", Name: "testFlaky"}, Outcome: OutcomeFlaky, FinalStatus: StatusPassed,
			Prior: []Event{{Status: StatusFailed, Message: "timeout"}}, Retries: 1, LastTime: 0.1},
	}
	out, err := WriteJUnit(tests, mustParseTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	xsdPath := filepath.Join(dir, "junit.xsd")
	xmlPath := filepath.Join(dir, "out.xml")
	_ = os.WriteFile(xsdPath, xsd, 0o644)
	_ = os.WriteFile(xmlPath, out, 0o644)

	cmd := exec.Command("xmllint", "--noout", "--schema", xsdPath, xmlPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected xmllint to reject the flakyFailure extension against the generic XSD, but it validated")
	}
}

// --- A minimal, stdlib-only JSON Schema (draft-07 subset) validator, just
// large enough to check this package's CTRF output against the real,
// fetched ctrf-io/ctrf schema: $ref (local only), type, required,
// properties, items, enum, const, pattern, minItems, minLength, minimum,
// additionalProperties, anyOf. Anything else in the schema is ignored
// rather than mis-enforced.

type jsonSchema = map[string]any

func resolveRef(root, node jsonSchema) jsonSchema {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	if len(ref) < 2 || ref[0] != '#' {
		return node
	}
	cur := root
	for _, part := range regexp.MustCompile(`/`).Split(ref[2:], -1) {
		next, ok := cur[part].(map[string]any)
		if !ok {
			return node
		}
		cur = next
	}
	return cur
}

func validateSchema(root, schema jsonSchema, value any, path string) []string {
	schema = resolveRef(root, schema)
	var errs []string

	if anyOf, ok := schema["anyOf"].([]any); ok {
		var subErrs [][]string
		ok := false
		for _, s := range anyOf {
			sub := validateSchema(root, s.(map[string]any), value, path)
			if len(sub) == 0 {
				ok = true
				break
			}
			subErrs = append(subErrs, sub)
		}
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: matched none of %d anyOf branches (first: %v)", path, len(anyOf), subErrs))
		}
		return errs
	}

	if want, ok := schema["const"]; ok {
		if fmt.Sprint(value) != fmt.Sprint(want) {
			errs = append(errs, fmt.Sprintf("%s: const mismatch: got %v want %v", path, value, want))
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(value) {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v not in enum %v", path, value, enum))
		}
	}
	if pat, ok := schema["pattern"].(string); ok {
		if s, ok := value.(string); ok {
			re := regexp.MustCompile(pat)
			if !re.MatchString(s) {
				errs = append(errs, fmt.Sprintf("%s: %q does not match pattern %q", path, s, pat))
			}
		}
	}

	switch t := schema["type"]; t {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected object", path))
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				key := r.(string)
				if _, present := obj[key]; !present {
					errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, key))
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, v := range obj {
			if props != nil {
				if propSchema, ok := props[k].(map[string]any); ok {
					errs = append(errs, validateSchema(root, propSchema, v, path+"/"+k)...)
					continue
				}
			}
			if ap, ok := schema["additionalProperties"]; ok {
				if b, ok := ap.(bool); ok && !b {
					errs = append(errs, fmt.Sprintf("%s: additional property %q not allowed", path, k))
				}
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected array", path))
		}
		if min, ok := schema["minItems"].(float64); ok && float64(len(arr)) < min {
			errs = append(errs, fmt.Sprintf("%s: has %d items, want >= %v", path, len(arr), min))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, v := range arr {
				errs = append(errs, validateSchema(root, items, v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected string", path))
		}
		if min, ok := schema["minLength"].(float64); ok && float64(len(s)) < min {
			errs = append(errs, fmt.Sprintf("%s: shorter than minLength %v", path, min))
		}
	case "integer", "number":
		n, ok := value.(float64)
		if !ok {
			return append(errs, fmt.Sprintf("%s: expected number", path))
		}
		if min, ok := schema["minimum"].(float64); ok && n < min {
			errs = append(errs, fmt.Sprintf("%s: %v below minimum %v", path, n, min))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected boolean", path))
		}
	}
	return errs
}

// TestWriteCTRF_ValidatesAgainstSchema validates the double-count fixture's
// merged CTRF output against the real ctrf-io/ctrf JSON schema, using a
// minimal in-repo draft-07 subset validator (see above) so this check needs
// no dependency beyond the stdlib.
func TestWriteCTRF_ValidatesAgainstSchema(t *testing.T) {
	schemaBytes := fetchPinned(t, ctrfSchemaURL, ctrfSchemaSHA256)
	var schema jsonSchema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse fetched CTRF schema: %v", err)
	}

	tests := []MergedTest{
		{Identity: Identity{Suite: "pkg", Name: "testFlaky"}, Outcome: OutcomeFlaky, FinalStatus: StatusPassed,
			Prior: []Event{{Status: StatusFailed, Message: "boom", Time: 0.5}}, Retries: 1, LastTime: 0.6, TotalTime: 1.1},
		{Identity: Identity{Suite: "pkg", Name: "testStable"}, Outcome: OutcomePassed, FinalStatus: StatusPassed, LastTime: 1.0, TotalTime: 1.0},
		{Identity: Identity{Suite: "pkg", Name: "testAlwaysFails"}, Outcome: OutcomeFailed, FinalStatus: StatusFailed,
			Primary: Event{Status: StatusFailed, Message: "nope", Time: 0.1}, LastTime: 0.1, TotalTime: 0.1},
	}
	out, err := WriteCTRF(tests, mustParseTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}

	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("our own CTRF output does not even parse as JSON: %v", err)
	}
	if errs := validateSchema(schema, schema, doc, "$"); len(errs) > 0 {
		t.Fatalf("CTRF output fails schema validation:\n%v\ninput:\n%s", errs, out)
	}
}
