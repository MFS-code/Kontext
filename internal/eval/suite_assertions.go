package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type suiteAssertionSpec struct {
	validate     func(SuiteAssertion, map[string]struct{}) error
	requirements func(SuiteAssertion, string, *artifactRequirements)
	evaluate     func(SuiteAssertion, []Record) SuiteAssertionResult
}

var suiteAssertionSpecs = map[SuiteAssertionType]suiteAssertionSpec{
	SuiteAssertionFieldsEqual: {
		validate:     validateFieldsEqualAssertion,
		requirements: requireAssertionFields,
		evaluate:     evaluateFieldsEqual,
	},
	SuiteAssertionForbiddenMarkers: {
		validate:     validateForbiddenMarkersAssertion,
		requirements: requireAssertionFields,
		evaluate:     evaluateForbiddenMarkers,
	},
}

type suiteRecordFieldSpec struct {
	value        func(Record) any
	requirements func(*artifactRequirements)
}

var suiteRecordFieldSpecs = map[string]suiteRecordFieldSpec{
	"terminalPhase": {
		value: func(record Record) any { return record.TerminalPhase },
	},
	"statusResult": {
		value: func(record Record) any { return record.StatusResult },
		requirements: func(requirements *artifactRequirements) {
			requirements.statusResult = true
		},
	},
	"statusOutput": {
		value: func(record Record) any { return record.StatusOutput },
		requirements: func(requirements *artifactRequirements) {
			requirements.statusOutput = true
		},
	},
	"statusUsage": {
		value: func(record Record) any { return record.StatusUsage },
		requirements: func(requirements *artifactRequirements) {
			requirements.statusUsage = true
		},
	},
	"podExitCode": {
		value: func(record Record) any { return record.PodExitCode },
		requirements: func(requirements *artifactRequirements) {
			requirements.pod = true
			requirements.exitCode = true
		},
	},
	"envelope": {
		value: func(record Record) any { return record.Envelope },
	},
	"events": {
		value: func(record Record) any { return record.Events },
	},
	"grades": {
		value: func(record Record) any { return record.Grades },
	},
	"judge": {
		value: func(record Record) any { return record.Judge },
	},
	"collectionErrors": {
		value: func(record Record) any { return record.CollectionErrors },
	},
	"durationMillis": {
		value: func(record Record) any { return record.DurationMillis },
	},
}

func suiteAssertionSpecFor(assertionType SuiteAssertionType) (suiteAssertionSpec, error) {
	spec, ok := suiteAssertionSpecs[assertionType]
	if !ok {
		return suiteAssertionSpec{}, fmt.Errorf("unsupported suite assertion type %q", assertionType)
	}
	if spec.validate == nil || spec.requirements == nil || spec.evaluate == nil {
		return suiteAssertionSpec{}, fmt.Errorf(
			"suite assertion type %q has an incomplete dispatch spec",
			assertionType,
		)
	}
	return spec, nil
}

func validateSuiteAssertion(assertion SuiteAssertion, caseIDs map[string]struct{}) error {
	spec, err := suiteAssertionSpecFor(assertion.Type)
	if err != nil {
		return err
	}
	return spec.validate(assertion, caseIDs)
}

func validateFieldsEqualAssertion(assertion SuiteAssertion, caseIDs map[string]struct{}) error {
	if len(assertion.Records) < 2 {
		return errors.New("records must contain at least two case IDs")
	}
	if len(assertion.Markers) != 0 {
		return errors.New("markers are not valid for fieldsEqual")
	}
	if err := validateAssertionRecords(assertion.Records, caseIDs); err != nil {
		return err
	}
	return validateAssertionFields(assertion.Fields)
}

func validateForbiddenMarkersAssertion(assertion SuiteAssertion, caseIDs map[string]struct{}) error {
	if len(assertion.Records) != 0 {
		if err := validateAssertionRecords(assertion.Records, caseIDs); err != nil {
			return err
		}
	}
	if err := validateAssertionFields(assertion.Fields); err != nil {
		return err
	}
	if len(assertion.Markers) == 0 {
		return errors.New("markers must not be empty")
	}
	seen := make(map[string]struct{}, len(assertion.Markers))
	for index, marker := range assertion.Markers {
		if strings.TrimSpace(marker) == "" {
			return fmt.Errorf("markers[%d] must not be blank", index)
		}
		if _, exists := seen[marker]; exists {
			return fmt.Errorf("duplicate marker %q", marker)
		}
		seen[marker] = struct{}{}
	}
	return nil
}

func validateAssertionRecords(records []string, caseIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(records))
	for index, caseID := range records {
		if strings.TrimSpace(caseID) == "" {
			return fmt.Errorf("records[%d] must not be blank", index)
		}
		if _, exists := caseIDs[caseID]; !exists {
			return fmt.Errorf("record case %q does not exist in spec.cases", caseID)
		}
		if _, exists := seen[caseID]; exists {
			return fmt.Errorf("duplicate record case %q", caseID)
		}
		seen[caseID] = struct{}{}
	}
	return nil
}

func validateAssertionFields(fields []string) error {
	if len(fields) == 0 {
		return errors.New("fields must not be empty")
	}
	seen := make(map[string]struct{}, len(fields))
	for index, field := range fields {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("fields[%d] must not be blank", index)
		}
		if _, exists := suiteRecordFieldSpecs[field]; !exists {
			return fmt.Errorf("unsupported record field %q", field)
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("duplicate record field %q", field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

func requirementsForSuiteAssertions(
	assertions []SuiteAssertion,
	caseID string,
	requirements *artifactRequirements,
) error {
	for _, assertion := range assertions {
		spec, err := suiteAssertionSpecFor(assertion.Type)
		if err != nil {
			return err
		}
		spec.requirements(assertion, caseID, requirements)
	}
	return nil
}

func requireAssertionFields(
	assertion SuiteAssertion,
	caseID string,
	requirements *artifactRequirements,
) {
	if !assertionTargetsCase(assertion, caseID) {
		return
	}
	for _, field := range assertion.Fields {
		if fieldSpec, exists := suiteRecordFieldSpecs[field]; exists &&
			fieldSpec.requirements != nil {
			fieldSpec.requirements(requirements)
		}
	}
}

func assertionTargetsCase(assertion SuiteAssertion, caseID string) bool {
	if len(assertion.Records) == 0 {
		return true
	}
	for _, target := range assertion.Records {
		if target == caseID {
			return true
		}
	}
	return false
}

func EvaluateSuiteAssertions(
	assertions []SuiteAssertion,
	records []Record,
) []SuiteAssertionResult {
	if len(assertions) == 0 {
		return nil
	}
	results := make([]SuiteAssertionResult, 0, len(assertions))
	for _, assertion := range assertions {
		spec, err := suiteAssertionSpecFor(assertion.Type)
		if err != nil {
			results = append(results, SuiteAssertionResult{
				Type:    assertion.Type,
				Records: assertion.Records,
				Fields:  assertion.Fields,
				Pass:    false,
				Message: err.Error(),
			})
			continue
		}
		results = append(results, spec.evaluate(assertion, records))
	}
	return results
}

func evaluateFieldsEqual(assertion SuiteAssertion, records []Record) SuiteAssertionResult {
	result := newSuiteAssertionResult(assertion)
	selected, err := selectAssertionRecords(assertion.Records, records)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	for _, field := range assertion.Fields {
		fieldSpec := suiteRecordFieldSpecs[field]
		reference := fieldSpec.value(selected[0])
		for index := 1; index < len(selected); index++ {
			if !reflect.DeepEqual(reference, fieldSpec.value(selected[index])) {
				result.Message = fmt.Sprintf(
					"field %q differs between records %q and %q",
					field,
					selected[0].CaseID,
					selected[index].CaseID,
				)
				return result
			}
		}
	}
	result.Pass = true
	result.Message = fmt.Sprintf(
		"%d field(s) matched across %d records",
		len(assertion.Fields),
		len(selected),
	)
	return result
}

func evaluateForbiddenMarkers(
	assertion SuiteAssertion,
	records []Record,
) SuiteAssertionResult {
	result := newSuiteAssertionResult(assertion)
	selected, err := selectAssertionRecords(assertion.Records, records)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	for _, record := range selected {
		for _, field := range assertion.Fields {
			encoded, err := encodeAssertionField(suiteRecordFieldSpecs[field].value(record))
			if err != nil {
				result.Message = fmt.Sprintf(
					"encode record %q field %q: %v",
					record.CaseID,
					field,
					err,
				)
				return result
			}
			for markerIndex, marker := range assertion.Markers {
				if bytes.Contains(encoded, []byte(marker)) {
					result.Message = fmt.Sprintf(
						"record %q field %q contains forbidden marker at index %d",
						record.CaseID,
						field,
						markerIndex,
					)
					return result
				}
			}
		}
	}
	result.Pass = true
	result.Message = fmt.Sprintf(
		"%d field(s) across %d record(s) contained no forbidden markers",
		len(assertion.Fields),
		len(selected),
	)
	return result
}

func encodeAssertionField(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func newSuiteAssertionResult(assertion SuiteAssertion) SuiteAssertionResult {
	return SuiteAssertionResult{
		Type:    assertion.Type,
		Records: append([]string(nil), assertion.Records...),
		Fields:  append([]string(nil), assertion.Fields...),
	}
}

func selectAssertionRecords(caseIDs []string, records []Record) ([]Record, error) {
	byCaseID := make(map[string]Record, len(records))
	for _, record := range records {
		byCaseID[record.CaseID] = record
	}
	if len(caseIDs) == 0 {
		if len(records) == 0 {
			return nil, errors.New("no evaluation records were available")
		}
		return records, nil
	}
	selected := make([]Record, 0, len(caseIDs))
	for _, caseID := range caseIDs {
		record, exists := byCaseID[caseID]
		if !exists {
			return nil, fmt.Errorf("evaluation record %q was absent", caseID)
		}
		selected = append(selected, record)
	}
	return selected, nil
}
