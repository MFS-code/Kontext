package config

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxTurnsLimit                = int64(10_000)
	maxToolCallsLimit            = int64(100_000)
	maxToolResultBytesLimit      = int64(8 << 20)
	maxTotalToolOutputBytesLimit = int64(64 << 20)
)

type Config struct {
	RunName   string
	AgentName string
	Goal      string
	Provider  string
	Model     string
	Tools     []string

	TokenBudget             *int64
	WallclockBudget         *time.Duration
	DollarBudget            *float64
	MaxTurns                *int64
	MaxToolCalls            *int64
	MaxToolResultBytes      *int64
	MaxTotalToolOutputBytes *int64

	ProviderEndpoint  string
	ProviderBaseURL   string
	AnthropicAPIKey   string
	OpenAIAPIKey      string
	FakeScenario      string
	FakeDelay         time.Duration
	FakeToolName      string
	FakeToolArguments string
}

func Load(getenv func(string) string) (Config, error) {
	goal, err := required(getenv, "KONTEXT_GOAL")
	if err != nil {
		return Config{}, err
	}
	provider, err := required(getenv, "KONTEXT_PROVIDER")
	if err != nil {
		return Config{}, err
	}
	model, err := requiredOpaque(getenv, "KONTEXT_MODEL")
	if err != nil {
		return Config{}, err
	}

	tokenBudget, err := optionalPositiveInt64(getenv("KONTEXT_BUDGET_TOKENS"), "KONTEXT_BUDGET_TOKENS")
	if err != nil {
		return Config{}, err
	}
	wallclockBudget, err := optionalPositiveDuration(
		getenv("KONTEXT_BUDGET_WALLCLOCK"),
		"KONTEXT_BUDGET_WALLCLOCK",
	)
	if err != nil {
		return Config{}, err
	}
	dollarBudget, err := optionalNonNegativeFloat(
		getenv("KONTEXT_BUDGET_DOLLARS"),
		"KONTEXT_BUDGET_DOLLARS",
	)
	if err != nil {
		return Config{}, err
	}
	maxTurns, err := optionalLimitInt64(
		getenv("KONTEXT_MAX_TURNS"),
		"KONTEXT_MAX_TURNS",
		maxTurnsLimit,
	)
	if err != nil {
		return Config{}, err
	}
	maxToolCalls, err := optionalLimitInt64(
		getenv("KONTEXT_MAX_TOOL_CALLS"),
		"KONTEXT_MAX_TOOL_CALLS",
		maxToolCallsLimit,
	)
	if err != nil {
		return Config{}, err
	}
	maxToolResultBytes, err := optionalLimitInt64(
		getenv("KONTEXT_MAX_TOOL_RESULT_BYTES"),
		"KONTEXT_MAX_TOOL_RESULT_BYTES",
		maxToolResultBytesLimit,
	)
	if err != nil {
		return Config{}, err
	}
	maxTotalToolOutputBytes, err := optionalLimitInt64(
		getenv("KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES"),
		"KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES",
		maxTotalToolOutputBytesLimit,
	)
	if err != nil {
		return Config{}, err
	}
	endpoint, err := optionalEndpoint(
		getenv("KONTEXT_PROVIDER_ENDPOINT"),
		"KONTEXT_PROVIDER_ENDPOINT",
	)
	if err != nil {
		return Config{}, err
	}
	baseURL, err := optionalEndpoint(
		getenv("KONTEXT_PROVIDER_BASE_URL"),
		"KONTEXT_PROVIDER_BASE_URL",
	)
	if err != nil {
		return Config{}, err
	}
	if endpoint != "" && baseURL != "" {
		return Config{}, fmt.Errorf(
			"KONTEXT_PROVIDER_ENDPOINT and KONTEXT_PROVIDER_BASE_URL are mutually exclusive",
		)
	}

	runName := strings.TrimSpace(getenv("KONTEXT_RUN_NAME"))
	if runName == "" {
		runName = "unknown-run"
	}
	agentName := strings.TrimSpace(getenv("KONTEXT_AGENT_NAME"))
	if agentName == "" {
		agentName = runName
	}

	// The fake-provider knobs are opaque strings here; the provider package
	// owns their meaning and validation.
	fakeScenario := strings.ToLower(strings.TrimSpace(getenv("KONTEXT_FAKE_SCENARIO")))
	fakeDelayValue, err := optionalPositiveDuration(getenv("KONTEXT_FAKE_DELAY"), "KONTEXT_FAKE_DELAY")
	if err != nil {
		return Config{}, err
	}
	var fakeDelay time.Duration
	if fakeDelayValue != nil {
		fakeDelay = *fakeDelayValue
	}

	return Config{
		RunName:                 runName,
		AgentName:               agentName,
		Goal:                    goal,
		Provider:                strings.ToLower(provider),
		Model:                   model,
		Tools:                   parseTools(getenv("KONTEXT_TOOLS")),
		TokenBudget:             tokenBudget,
		WallclockBudget:         wallclockBudget,
		DollarBudget:            dollarBudget,
		MaxTurns:                maxTurns,
		MaxToolCalls:            maxToolCalls,
		MaxToolResultBytes:      maxToolResultBytes,
		MaxTotalToolOutputBytes: maxTotalToolOutputBytes,
		ProviderEndpoint:        endpoint,
		ProviderBaseURL:         baseURL,
		AnthropicAPIKey:         getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:            getenv("OPENAI_API_KEY"),
		FakeScenario:            fakeScenario,
		FakeDelay:               fakeDelay,
		FakeToolName:            strings.TrimSpace(getenv("KONTEXT_FAKE_TOOL_NAME")),
		FakeToolArguments:       strings.TrimSpace(getenv("KONTEXT_FAKE_TOOL_ARGUMENTS")),
	}, nil
}

func optionalLimitInt64(value string, name string, maximum int64) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", name)
	}
	if parsed == 0 {
		return nil, nil
	}
	if parsed > maximum {
		return nil, fmt.Errorf("%s must not exceed %d", name, maximum)
	}
	return &parsed, nil
}

func required(getenv func(string) string, name string) (string, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func requiredOpaque(getenv func(string) string, name string) (string, error) {
	value := getenv(name)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func optionalPositiveInt64(value string, name string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", name)
	}
	if parsed == 0 {
		return nil, nil
	}
	return &parsed, nil
}

func optionalPositiveDuration(value string, name string) (*time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative duration", name)
	}
	if parsed == 0 {
		return nil, nil
	}
	return &parsed, nil
}

func optionalNonNegativeFloat(value string, name string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil, fmt.Errorf("%s must be a non-negative number", name)
	}
	return &parsed, nil
}

func optionalEndpoint(value string, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%s must not contain embedded credentials", name)
	}
	return value, nil
}

func parseTools(value string) []string {
	var tools []string
	for _, tool := range strings.Split(value, ",") {
		if normalized := strings.TrimSpace(tool); normalized != "" {
			tools = append(tools, normalized)
		}
	}
	return tools
}
