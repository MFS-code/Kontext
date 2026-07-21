// Package scheduler implements deterministic Scheduled-mode cron calculations.
package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

const (
	DefaultTimeZone                  = "Etc/UTC"
	DefaultStartingDeadlineSeconds   = int64(60)
	DefaultSuccessfulRunsHistory     = int32(3)
	DefaultFailedRunsHistory         = int32(1)
	maxKubernetesObjectNameLength    = 63
	truncatedAgentNameHashCharacters = 8
)

var (
	standardParser          = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	embeddedTimeZonePattern = regexp.MustCompile(`(^|[[:space:]])(CRON_TZ|TZ)=`)
)

// Clock is the portion of a clock required by scheduling reconciliation.
type Clock interface {
	Now() time.Time
}

// RealClock reads the system clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time {
	return time.Now()
}

// Policy is a ScheduleSpec with API defaults applied.
type Policy struct {
	Schedule                   cron.Schedule
	TimeZone                   *time.Location
	ConcurrencyPolicy          kontextv1alpha1.ConcurrencyPolicy
	StartingDeadline           time.Duration
	Suspend                    bool
	SuccessfulRunsHistoryLimit int32
	FailedRunsHistoryLimit     int32
}

// Parse validates a ScheduleSpec and returns its executable policy.
func Parse(spec *kontextv1alpha1.ScheduleSpec) (Policy, error) {
	if spec == nil {
		return Policy{}, fmt.Errorf("spec.schedule is required")
	}

	expression := strings.TrimSpace(spec.Expression)
	if embeddedTimeZonePattern.MatchString(expression) {
		return Policy{}, fmt.Errorf("schedule.expression must not contain TZ or CRON_TZ")
	}

	timeZoneName := spec.TimeZone
	if timeZoneName == "" {
		timeZoneName = DefaultTimeZone
	}
	location, err := time.LoadLocation(timeZoneName)
	if err != nil {
		return Policy{}, fmt.Errorf("invalid IANA schedule.timeZone %q: %w", timeZoneName, err)
	}

	schedule, err := standardParser.Parse("CRON_TZ=" + timeZoneName + " " + expression)
	if err != nil {
		return Policy{}, fmt.Errorf("invalid standard five-field schedule.expression %q: %w", expression, err)
	}

	concurrencyPolicy := spec.ConcurrencyPolicy
	if concurrencyPolicy == "" {
		concurrencyPolicy = kontextv1alpha1.ConcurrencyPolicyForbid
	}
	switch concurrencyPolicy {
	case kontextv1alpha1.ConcurrencyPolicyAllow, kontextv1alpha1.ConcurrencyPolicyForbid:
	default:
		return Policy{}, fmt.Errorf("unsupported schedule.concurrencyPolicy %q", concurrencyPolicy)
	}

	deadlineSeconds := DefaultStartingDeadlineSeconds
	if spec.StartingDeadlineSeconds != nil {
		deadlineSeconds = *spec.StartingDeadlineSeconds
	}
	if deadlineSeconds < 0 {
		return Policy{}, fmt.Errorf("schedule.startingDeadlineSeconds must not be negative")
	}

	successLimit := DefaultSuccessfulRunsHistory
	if spec.SuccessfulRunsHistoryLimit != nil {
		successLimit = *spec.SuccessfulRunsHistoryLimit
	}
	failureLimit := DefaultFailedRunsHistory
	if spec.FailedRunsHistoryLimit != nil {
		failureLimit = *spec.FailedRunsHistoryLimit
	}
	if successLimit < 0 || failureLimit < 0 {
		return Policy{}, fmt.Errorf("schedule history limits must not be negative")
	}

	return Policy{
		Schedule:                   schedule,
		TimeZone:                   location,
		ConcurrencyPolicy:          concurrencyPolicy,
		StartingDeadline:           time.Duration(deadlineSeconds) * time.Second,
		Suspend:                    spec.Suspend,
		SuccessfulRunsHistoryLimit: successLimit,
		FailedRunsHistoryLimit:     failureLimit,
	}, nil
}

// LatestDue returns the latest slot at or before now, starting at firstDue,
// plus the first future slot. It intentionally returns only one due slot.
func LatestDue(schedule cron.Schedule, firstDue time.Time, now time.Time) (due time.Time, next time.Time) {
	if firstDue.After(now) {
		return time.Time{}, firstDue
	}

	due = firstDue
	next = schedule.Next(due)
	for !next.After(now) {
		due = next
		next = schedule.Next(next)
	}
	return due, next
}

// RunName derives a stable DNS label from the Agent name and scheduled slot.
func RunName(agentName string, slot time.Time) string {
	slotSuffix := fmt.Sprintf("-%d", slot.Unix())
	if len(agentName)+len(slotSuffix) <= maxKubernetesObjectNameLength {
		return agentName + slotSuffix
	}

	sum := sha256.Sum256([]byte(agentName))
	hash := hex.EncodeToString(sum[:])[:truncatedAgentNameHashCharacters]
	hashSuffix := "-" + hash + slotSuffix
	prefixLength := maxKubernetesObjectNameLength - len(hashSuffix)
	prefix := strings.TrimRight(agentName[:prefixLength], "-")
	return prefix + hashSuffix
}
