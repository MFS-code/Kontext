package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RunName   string
	AgentName string
	Goal      string
	Provider  string
	Model     string
	Tools     []string

	TokenBudget     *int64
	WallclockBudget *time.Duration
	DollarBudget    *float64

	ProviderEndpoint string
	FakeScenario     string
	FakeDelay        time.Duration
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
	model, err := required(getenv, "KONTEXT_MODEL")
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
	endpoint, err := optionalEndpoint(getenv("KONTEXT_PROVIDER_ENDPOINT"))
	if err != nil {
		return Config{}, err
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
		RunName:          runName,
		AgentName:        agentName,
		Goal:             goal,
		Provider:         strings.ToLower(provider),
		Model:            model,
		Tools:            parseTools(getenv("KONTEXT_TOOLS")),
		TokenBudget:      tokenBudget,
		WallclockBudget:  wallclockBudget,
		DollarBudget:     dollarBudget,
		ProviderEndpoint: endpoint,
		FakeScenario:     fakeScenario,
		FakeDelay:        fakeDelay,
	}, nil
}

func required(getenv func(string) string, name string) (string, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
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
	if err != nil || parsed <= 0 {
		return nil, fmt.Errorf("%s must be a positive integer", name)
	}
	return &parsed, nil
}

func optionalPositiveDuration(value string, name string) (*time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return nil, fmt.Errorf("%s must be a positive duration", name)
	}
	return &parsed, nil
}

func optionalNonNegativeFloat(value string, name string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative number", name)
	}
	return &parsed, nil
}

func optionalEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("KONTEXT_PROVIDER_ENDPOINT must be an absolute HTTP(S) URL")
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
