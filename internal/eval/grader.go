package eval

func GradeRecord(record *Record, graders []Grader) {
	record.Grades = make([]Grade, 0, len(graders))
	for _, grader := range graders {
		record.Grades = append(record.Grades, grade(record, grader))
	}
}

func grade(record *Record, grader Grader) Grade {
	spec, err := graderSpecFor(grader.Type)
	if err != nil {
		return Grade{Type: grader.Type, Message: err.Error()}
	}
	result := spec.grade(record, grader)
	if !result.Pass && result.Message == "" {
		result.Message = "observed value did not match expectation"
	}
	return result
}

func usagePresence(record *Record) map[string]bool {
	presence := map[string]bool{
		"tokens": false, "inputTokens": false, "outputTokens": false, "dollars": false,
	}
	if record.StatusUsage == nil {
		return presence
	}
	presence["tokens"] = record.StatusUsage.Tokens != nil
	presence["inputTokens"] = record.StatusUsage.InputTokens != nil
	presence["outputTokens"] = record.StatusUsage.OutputTokens != nil
	presence["dollars"] = record.StatusUsage.Dollars != nil
	return presence
}

func matchingTools(tools []ToolEvent, expectation ToolExpectation) []ToolEvent {
	var matches []ToolEvent
	for _, tool := range tools {
		if tool.Name != expectation.Name {
			continue
		}
		if expectation.IsError != nil && tool.IsError != *expectation.IsError {
			continue
		}
		if expectation.ErrorCode != "" && tool.ErrorCode != expectation.ErrorCode {
			continue
		}
		if expectation.Truncated != nil && tool.Truncated != *expectation.Truncated {
			continue
		}
		matches = append(matches, tool)
	}
	return matches
}

func gradesPass(grades []Grade) bool {
	for _, item := range grades {
		if !item.Pass {
			return false
		}
	}
	return true
}
