// Package policy holds the server's public rules, published by GET /policy.
package policy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

// Public is the body of GET /policy. It is a flat object of public rules;
// new keys may be added, so clients must ignore keys they do not know.
func Public() map[string]any {
	guests, _ := GuestsAllowed()
	return map[string]any{"guests_allowed": guests}
}
