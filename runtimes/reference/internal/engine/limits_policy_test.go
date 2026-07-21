package engine

import (
	"math"
	"testing"
)

func TestLimitsPolicyBoundaries(t *testing.T) {
	const limit = int64(2)
	tests := []struct {
		name     string
		check    func(limitsPolicy, *loopState) *limitViolation
		policy   limitsPolicy
		state    loopState
		wantCode string
	}{
		{
			name: "provider turn below limit",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkBeforeProviderTurn(state)
			},
			policy: limitsPolicy{maxTurns: int64Pointer(limit)},
			state:  loopState{turns: 1},
		},
		{
			name: "provider turn at limit",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkBeforeProviderTurn(state)
			},
			policy:   limitsPolicy{maxTurns: int64Pointer(limit)},
			state:    loopState{turns: 2},
			wantCode: "turn_limit_exceeded",
		},
		{
			name: "response at token budget",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkAfterResponse(state)
			},
			policy: limitsPolicy{tokenBudget: int64Pointer(limit)},
			state:  loopState{consumedTokens: 2},
		},
		{
			name: "response over token budget",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkAfterResponse(state)
			},
			policy:   limitsPolicy{tokenBudget: int64Pointer(limit)},
			state:    loopState{consumedTokens: 3},
			wantCode: "token_limit_exceeded",
		},
		{
			name: "tool follow-up at token budget",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkBeforeToolBatch(state, 1)
			},
			policy:   limitsPolicy{tokenBudget: int64Pointer(limit)},
			state:    loopState{consumedTokens: 2},
			wantCode: "token_limit_exceeded",
		},
		{
			name: "tool batch exactly fits",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkBeforeToolBatch(state, 1)
			},
			policy: limitsPolicy{maxToolCalls: int64Pointer(limit)},
			state:  loopState{toolCalls: 1},
		},
		{
			name: "tool batch exceeds remaining",
			check: func(policy limitsPolicy, state *loopState) *limitViolation {
				return policy.checkBeforeToolBatch(state, 2)
			},
			policy:   limitsPolicy{maxToolCalls: int64Pointer(limit)},
			state:    loopState{toolCalls: 1},
			wantCode: "tool_call_limit_exceeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			violation := test.check(test.policy, &test.state)
			if test.wantCode == "" {
				if violation != nil {
					t.Fatalf("unexpected violation %#v", violation)
				}
				return
			}
			if violation == nil || violation.code != test.wantCode {
				t.Fatalf("violation = %#v, want code %q", violation, test.wantCode)
			}
		})
	}
}

func TestSaturatingAddBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		left       int64
		right      int64
		wantResult int64
	}{
		{name: "ordinary", left: 20, right: 22, wantResult: 42},
		{name: "positive overflow", left: math.MaxInt64 - 1, right: 2, wantResult: math.MaxInt64},
		{name: "negative overflow", left: math.MinInt64 + 1, right: -2, wantResult: math.MinInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if result := saturatingAdd(test.left, test.right); result != test.wantResult {
				t.Fatalf("saturatingAdd(%d, %d) = %d, want %d", test.left, test.right, result, test.wantResult)
			}
		})
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
