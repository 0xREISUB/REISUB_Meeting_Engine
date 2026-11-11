// Package policy holds the server's public rules, published by GET /policy.
package policy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// GuestsAllowed reports whether meetings may be used without an account
// (GUESTS_ALLOWED, default true). A value that is not a boolean disallows
// guests, so a typo never opens the server by accident; err describes it.
func GuestsAllowed() (bool, error) {
	value := strings.TrimSpace(os.Getenv("GUESTS_ALLOWED"))
	if value == "" {
		return true, nil
	}
	allowed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("GUESTS_ALLOWED=%q is not true or false; guests are not allowed", value)
	}
	return allowed, nil
}

// minutes reads a whole number of minutes from the environment: 0 (off)
// when unset; err describes a value that is not a non-negative integer,
// which also counts as 0.
func minutes(key string) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s=%q is not a whole number of minutes; using 0 (off)", key, value)
	}
	return n, nil
}

// MeetingTimeLimit is how long a new room may last
// (MEETING_TIME_LIMIT_MINUTES); 0 means no limit.
func MeetingTimeLimit() time.Duration {
	n, _ := minutes("MEETING_TIME_LIMIT_MINUTES")
	return time.Duration(n) * time.Minute
}

// MeetingCooldown is how long the creator and owner of a room that ended at
// its time limit wait before creating another (MEETING_COOLDOWN_MINUTES);
// 0 means no wait.
func MeetingCooldown() time.Duration {
	n, _ := minutes("MEETING_COOLDOWN_MINUTES")
	return time.Duration(n) * time.Minute
}

// Problems lists settings that could not be read, for a startup warning.
func Problems() []error {
	var problems []error
	if _, err := GuestsAllowed(); err != nil {
		problems = append(problems, err)
	}
	for _, key := range []string{"MEETING_TIME_LIMIT_MINUTES", "MEETING_COOLDOWN_MINUTES"} {
		if _, err := minutes(key); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}

// Public is the body of GET /policy. It is a flat object of public rules;
// new keys may be added, so clients must ignore keys they do not know.
func Public() map[string]any {
	guests, _ := GuestsAllowed()
	return map[string]any{
		"guests_allowed":             guests,
		"meeting_time_limit_minutes": int(MeetingTimeLimit() / time.Minute),
		"meeting_cooldown_minutes":   int(MeetingCooldown() / time.Minute),
	}
}
