package eval

import (
	"strings"
	"testing"
)

func TestSuiteAssertionDispatchCoversEveryType(t *testing.T) {
	assertions := []SuiteAssertion{
		{
			Type:    SuiteAssertionFieldsEqual,
			Records: []string{"a", "b"},
			Fields:  []string{"statusResult"},
		},
		{
			Type:    SuiteAssertionForbiddenMarkers,
			Fields:  []string{"statusResult"},
			Markers: []string{"secret-marker"},
		},
	}
	caseIDs := map[string]struct{}{"a": {}, "b": {}}
	if len(suiteAssertionSpecs) != len(assertions) {
		t.Fatalf(
			"dispatch has %d types, test covers %d",
			len(suiteAssertionSpecs),
			len(assertions),
		)
	}
	for _, assertion := range assertions {
		t.Run(string(assertion.Type), func(t *testing.T) {
			if err := validateSuiteAssertion(assertion, caseIDs); err != nil {
				t.Fatalf("validateSuiteAssertion: %v", err)
			}
			spec, err := suiteAssertionSpecFor(assertion.Type)
			if err != nil {
				t.Fatalf("suiteAssertionSpecFor: %v", err)
			}
			requirements := artifactRequirements{}
			spec.requirements(assertion, "a", &requirements)
			result := spec.evaluate(assertion, []Record{
				{CaseID: "a", StatusResult: "same"},
				{CaseID: "b", StatusResult: "same"},
			})
			if !result.Pass {
				t.Fatalf("valid assertion failed: %#v", result)
			}
		})
	}
}

func TestParseSuiteRejectsInvalidSuiteAssertions(t *testing.T) {
	tests := map[string]struct {
		assertion string
		want      string
	}{
		"unknown type": {
			assertion: `
    - type: futureAssertion
      fields: [statusResult]
`,
			want: `unsupported suite assertion type "futureAssertion"`,
		},
		"missing case": {
			assertion: `
    - type: fieldsEqual
      records: [prompt-model-a, absent-case]
      fields: [statusResult]
`,
			want: `record case "absent-case" does not exist`,
		},
		"missing field": {
			assertion: `
    - type: fieldsEqual
      records: [prompt-model-a, prompt-model-b]
      fields: []
`,
			want: "fields must not be empty",
		},
		"unknown field": {
			assertion: `
    - type: forbiddenMarkers
      fields: [rawLogs]
      markers: [secret]
`,
			want: `unsupported record field "rawLogs"`,
		},
		"missing marker": {
			assertion: `
    - type: forbiddenMarkers
      fields: [statusResult]
`,
			want: "markers must not be empty",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			input := strings.Replace(
				validSuiteYAML,
				"  cases:\n",
				"  assertions:\n"+test.assertion+"  cases:\n",
				1,
			)
			_, err := ParseSuite([]byte(input), "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseSuite error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFieldsEqualAssertionReportsAbsentRecordAndMismatch(t *testing.T) {
	assertion := SuiteAssertion{
		Type:    SuiteAssertionFieldsEqual,
		Records: []string{"a", "b"},
		Fields:  []string{"statusResult"},
	}
	for name, test := range map[string]struct {
		records []Record
		want    string
	}{
		"absent record": {
			records: []Record{{CaseID: "a", StatusResult: "same"}},
			want:    `evaluation record "b" was absent`,
		},
		"mismatch": {
			records: []Record{
				{CaseID: "a", StatusResult: "first"},
				{CaseID: "b", StatusResult: "second"},
			},
			want: `field "statusResult" differs`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := EvaluateSuiteAssertions([]SuiteAssertion{assertion}, test.records)
			if len(result) != 1 || result[0].Pass ||
				!strings.Contains(result[0].Message, test.want) {
				t.Fatalf("unexpected assertion result: %#v", result)
			}
		})
	}
}

func TestForbiddenMarkersScansDeclaredRecordsAndFields(t *testing.T) {
	assertion := SuiteAssertion{
		Type:    SuiteAssertionForbiddenMarkers,
		Records: []string{"a", "b"},
		Fields:  []string{"statusResult", "collectionErrors"},
		Markers: []string{"credential-placeholder"},
	}
	records := []Record{
		{CaseID: "a", StatusResult: "safe"},
		{CaseID: "b", CollectionErrors: []string{"leaked credential-placeholder"}},
	}
	result := EvaluateSuiteAssertions([]SuiteAssertion{assertion}, records)
	if len(result) != 1 || result[0].Pass ||
		!strings.Contains(result[0].Message, `record "b" field "collectionErrors"`) ||
		strings.Contains(result[0].Message, assertion.Markers[0]) {
		t.Fatalf("marker was not reported clearly: %#v", result)
	}

	records[1].CollectionErrors = []string{"redacted"}
	result = EvaluateSuiteAssertions([]SuiteAssertion{assertion}, records)
	if len(result) != 1 || !result[0].Pass {
		t.Fatalf("clean records failed marker assertion: %#v", result)
	}
}

func TestSuiteAssertionsDriveOnlyDeclaredArtifactCollection(t *testing.T) {
	assertions := []SuiteAssertion{
		{
			Type:    SuiteAssertionFieldsEqual,
			Records: []string{"a", "b"},
			Fields:  []string{"statusResult"},
		},
		{
			Type:    SuiteAssertionForbiddenMarkers,
			Records: []string{"b"},
			Fields:  []string{"statusOutput"},
			Markers: []string{"secret"},
		},
	}
	for caseID, want := range map[string]struct {
		result bool
		output bool
	}{
		"a": {result: true},
		"b": {result: true, output: true},
		"c": {},
	} {
		t.Run(caseID, func(t *testing.T) {
			requirements := artifactRequirements{}
			if err := requirementsForSuiteAssertions(assertions, caseID, &requirements); err != nil {
				t.Fatal(err)
			}
			if requirements.statusResult != want.result ||
				requirements.statusOutput != want.output ||
				requirements.pod || requirements.logs || requirements.envelope {
				t.Fatalf("unexpected requirements: %#v", requirements)
			}
		})
	}
}
