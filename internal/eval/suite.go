package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

const defaultTimeout = 5 * time.Minute

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)

func LoadSuite(path string, runtimeImageOverride string) (EvalSuite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalSuite{}, err
	}
	return ParseSuite(data, runtimeImageOverride)
}

func ParseSuite(data []byte, runtimeImageOverride string) (EvalSuite, error) {
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return EvalSuite{}, fmt.Errorf("decode suite YAML: %w", err)
	}
	var suite EvalSuite
	decoder := json.NewDecoder(bytes.NewReader(jsonData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return EvalSuite{}, fmt.Errorf("decode suite: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return EvalSuite{}, errors.New("decode suite: trailing JSON value")
		}
		return EvalSuite{}, fmt.Errorf("decode suite trailing data: %w", err)
	}
	if err := prepareSuite(&suite, strings.TrimSpace(runtimeImageOverride)); err != nil {
		return EvalSuite{}, err
	}
	return suite, nil
}

func prepareSuite(suite *EvalSuite, runtimeImageOverride string) error {
	if suite.APIVersion != APIVersion {
		return fmt.Errorf("unsupported suite apiVersion %q", suite.APIVersion)
	}
	if suite.Kind != Kind {
		return fmt.Errorf("unsupported suite kind %q", suite.Kind)
	}
	if strings.TrimSpace(suite.Metadata.Name) == "" {
		return errors.New("metadata.name is required")
	}
	if len(suite.Spec.Cases) == 0 {
		return errors.New("spec.cases must not be empty")
	}
	if suite.Spec.Defaults.Timeout == nil {
		suite.Spec.Defaults.Timeout = &Duration{Duration: defaultTimeout}
	}
	if suite.Spec.Defaults.Timeout.Duration <= 0 {
		return errors.New("spec.defaults.timeout must be greater than zero")
	}
	if suite.Spec.Defaults.Namespace == "" {
		suite.Spec.Defaults.Namespace = "default"
	}
	seen := make(map[string]struct{}, len(suite.Spec.Cases))
	for index := range suite.Spec.Cases {
		item := &suite.Spec.Cases[index]
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			return fmt.Errorf("spec.cases[%d].id is required", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate case id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Timeout == nil {
			item.Timeout = suite.Spec.Defaults.Timeout
		}
		if item.Timeout.Duration <= 0 {
			return fmt.Errorf("case %q timeout must be greater than zero", item.ID)
		}
		if runtimeImageOverride != "" {
			item.AgentRun.Runtime.Image = runtimeImageOverride
		} else if item.AgentRun.Runtime.Image == "" {
			item.AgentRun.Runtime.Image = suite.Spec.Defaults.RuntimeImage
		}
		if strings.TrimSpace(item.AgentRun.Runtime.Image) == "" {
			return fmt.Errorf("case %q requires agentRun.runtime.image", item.ID)
		}
		if strings.TrimSpace(item.AgentRun.Model) == "" {
			return fmt.Errorf("case %q requires agentRun.model", item.ID)
		}
		if strings.TrimSpace(item.AgentRun.Goal) == "" {
			return fmt.Errorf("case %q requires agentRun.goal", item.ID)
		}
		if len(item.Graders) == 0 {
			return fmt.Errorf("case %q requires at least one deterministic grader", item.ID)
		}
		for graderIndex, grader := range item.Graders {
			if err := validateGrader(grader); err != nil {
				return fmt.Errorf("case %q grader %d: %w", item.ID, graderIndex, err)
			}
		}
	}
	for assertionIndex, assertion := range suite.Spec.Assertions {
		if err := validateSuiteAssertion(assertion, seen); err != nil {
			return fmt.Errorf("spec.assertions[%d]: %w", assertionIndex, err)
		}
	}
	return nil
}

func NameForCase(suiteName, caseID, invocation string) string {
	raw := suiteName + "\x00" + caseID + "\x00" + invocation
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:10]
	readable := strings.ToLower(strings.Join([]string{suiteName, caseID, invocation}, "-"))
	readable = strings.Trim(invalidDNSChars.ReplaceAllString(readable, "-"), "-")
	if readable == "" {
		readable = "eval"
	}
	maxReadable := 63 - len(digest) - 1
	if len(readable) > maxReadable {
		readable = strings.Trim(readable[:maxReadable], "-")
	}
	if readable == "" {
		readable = "eval"
	}
	return readable + "-" + digest
}
