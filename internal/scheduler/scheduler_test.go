package scheduler_test

import (
	"strings"
	"testing"
	"time"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/scheduler"
)

func TestParseAppliesDefaultsAndStandardFiveFieldSemantics(t *testing.T) {
	policy, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{Expression: "*/5 * * * *"})
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}
	if policy.TimeZone.String() != scheduler.DefaultTimeZone {
		t.Fatalf("time zone = %q, want %q", policy.TimeZone, scheduler.DefaultTimeZone)
	}
	if policy.ConcurrencyPolicy != kontextv1alpha1.ConcurrencyPolicyForbid {
		t.Fatalf("concurrency policy = %q", policy.ConcurrencyPolicy)
	}
	if policy.StartingDeadline != time.Minute ||
		policy.SuccessfulRunsHistoryLimit != 3 ||
		policy.FailedRunsHistoryLimit != 1 {
		t.Fatalf("defaults were not applied: %#v", policy)
	}

	start := time.Date(2026, time.July, 20, 12, 1, 30, 0, time.UTC)
	want := time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC)
	if got := policy.Schedule.Next(start); !got.Equal(want) {
		t.Fatalf("next slot = %s, want %s", got, want)
	}

	for _, test := range []struct {
		name       string
		expression string
	}{
		{name: "four fields", expression: "0 0 1 1"},
		{name: "six fields", expression: "0 0 0 1 1 1"},
		{name: "descriptor", expression: "@hourly"},
		{name: "range", expression: "61 * * * *"},
		{name: "leading CRON_TZ", expression: "CRON_TZ=Europe/Paris 0 * * * *"},
		{name: "leading TZ", expression: "TZ=UTC 0 * * * *"},
		{name: "leading tab TZ", expression: "\tTZ=UTC * * * *"},
		{name: "embedded tab TZ", expression: "*\tTZ=UTC * * *"},
		{name: "leading newline CRON_TZ", expression: "\nCRON_TZ=UTC * * * *"},
		{name: "embedded newline CRON_TZ", expression: "*\nCRON_TZ=UTC * * *"},
		{name: "embedded carriage return TZ", expression: "*\rTZ=UTC * * *"},
		{name: "embedded form feed CRON_TZ", expression: "*\fCRON_TZ=UTC * * *"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{Expression: test.expression}); err == nil {
				t.Fatalf("expected %q to be rejected", test.expression)
			}
		})
	}
}

func TestParseValidatesTimeZoneAndExplicitZeroLimits(t *testing.T) {
	zero64 := int64(0)
	zero32 := int32(0)
	policy, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{
		Expression:                 "0 * * * *",
		TimeZone:                   "Europe/Paris",
		ConcurrencyPolicy:          kontextv1alpha1.ConcurrencyPolicyAllow,
		StartingDeadlineSeconds:    &zero64,
		SuccessfulRunsHistoryLimit: &zero32,
		FailedRunsHistoryLimit:     &zero32,
	})
	if err != nil {
		t.Fatalf("parse explicit policy: %v", err)
	}
	if policy.TimeZone.String() != "Europe/Paris" ||
		policy.ConcurrencyPolicy != kontextv1alpha1.ConcurrencyPolicyAllow ||
		policy.StartingDeadline != 0 ||
		policy.SuccessfulRunsHistoryLimit != 0 ||
		policy.FailedRunsHistoryLimit != 0 {
		t.Fatalf("explicit policy changed: %#v", policy)
	}

	if _, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{
		Expression: "0 * * * *",
		TimeZone:   "Mars/Olympus_Mons",
	}); err == nil || !strings.Contains(err.Error(), "invalid IANA") {
		t.Fatalf("expected invalid IANA zone, got %v", err)
	}
}

func TestScheduleUsesFixedDSTTransitions(t *testing.T) {
	spring, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{
		Expression: "30 2 * * *",
		TimeZone:   "America/New_York",
	})
	if err != nil {
		t.Fatalf("parse spring schedule: %v", err)
	}
	beforeSpring := time.Date(2026, time.March, 8, 1, 59, 0, 0, spring.TimeZone)
	wantSpring := time.Date(2026, time.March, 9, 2, 30, 0, 0, spring.TimeZone)
	if got := spring.Schedule.Next(beforeSpring); !got.Equal(wantSpring) {
		t.Fatalf("spring next = %s, want %s", got, wantSpring)
	}

	fall, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{
		Expression: "30 1 * * *",
		TimeZone:   "America/New_York",
	})
	if err != nil {
		t.Fatalf("parse fall schedule: %v", err)
	}
	beforeFall := time.Date(2026, time.November, 1, 0, 59, 0, 0, fall.TimeZone)
	first := fall.Schedule.Next(beforeFall)
	second := fall.Schedule.Next(first)
	if first.Hour() != 1 || first.Minute() != 30 || first.Format("-07:00") != "-04:00" {
		t.Fatalf("first fall slot = %s, want first 01:30 EDT", first)
	}
	if second.Hour() != 1 || second.Minute() != 30 || second.Format("-07:00") != "-05:00" {
		t.Fatalf("second fall slot = %s, want repeated 01:30 EST", second)
	}
}

func TestLatestDueReturnsOneLatestSlotWithoutBackfill(t *testing.T) {
	policy, err := scheduler.Parse(&kontextv1alpha1.ScheduleSpec{Expression: "* * * * *"})
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}
	first := time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC)
	now := time.Date(2026, time.July, 20, 12, 5, 20, 0, time.UTC)
	due, next := scheduler.LatestDue(policy.Schedule, first, now)
	if want := time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("latest due = %s, want %s", due, want)
	}
	if want := time.Date(2026, time.July, 20, 12, 6, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestRunNameIsDeterministicAndCollisionResistantWhenTruncated(t *testing.T) {
	slot := time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC)
	if got := scheduler.RunName("short-agent", slot); got != "short-agent-1784549100" {
		t.Fatalf("short name = %q", got)
	}

	firstAgent := strings.Repeat("a", 62)
	secondAgent := strings.Repeat("a", 61) + "b"
	first := scheduler.RunName(firstAgent, slot)
	if first != scheduler.RunName(firstAgent, slot) {
		t.Fatal("same agent and slot produced different names")
	}
	if len(first) > 63 || first == scheduler.RunName(secondAgent, slot) {
		t.Fatalf("unsafe truncated names: first=%q second=%q", first, scheduler.RunName(secondAgent, slot))
	}
}
