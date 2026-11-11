package policy

import "testing"

func TestGuestsAllowed(t *testing.T) {
	for _, tc := range []struct {
		value   string
		allowed bool
		invalid bool
	}{
		{"", true, false}, // the default
		{"true", true, false},
		{" TRUE ", true, false},
		{"1", true, false},
		{"false", false, false},
		{"0", false, false},
		{"no", false, true}, // a typo never opens the server
	} {
		t.Setenv("GUESTS_ALLOWED", tc.value)
		allowed, err := GuestsAllowed()
		if allowed != tc.allowed || (err != nil) != tc.invalid {
			t.Errorf("%q: allowed=%v err=%v", tc.value, allowed, err)
		}
	}
}
