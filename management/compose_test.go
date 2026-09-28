package management

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestLimitedBufferRetainsOnlyTail(t *testing.T) {
	buffer := &limitedBuffer{limit: 8}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("defghijkl")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "efghijkl" {
		t.Fatalf("buffer tail = %q", got)
	}
	largeChunk := bytes.Repeat([]byte("x"), 64)
	if _, err := buffer.Write(largeChunk); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); len(got) != 8 || strings.Trim(got, "x") != "" {
		t.Fatalf("large chunk was not safely truncated: %q", got)
	}
}

func TestValidateServiceUsesFixedAllowlist(t *testing.T) {
	for _, service := range []string{"backend", "livekit"} {
		if err := validateService(service); err != nil {
			t.Fatalf("expected service %q to be allowed: %v", service, err)
		}
	}
	for _, service := range []string{"manager", "../../docker.sock", "backend;id"} {
		if err := validateService(service); err == nil {
			t.Fatalf("expected service %q to be rejected", service)
		}
	}
}

func TestActionArgumentsCreateAbsentServices(t *testing.T) {
	tests := []struct {
		action string
		want   []string
	}{
		{action: "start", want: []string{"up", "--detach", "--no-build", "backend"}},
		{action: "stop", want: []string{"stop", "backend"}},
		{action: "restart", want: []string{"up", "--detach", "--no-build", "--force-recreate", "backend"}},
	}
	for _, test := range tests {
		got, err := actionArguments("backend", test.action)
		if err != nil {
			t.Fatalf("actionArguments(%q): %v", test.action, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("actionArguments(%q) = %#v, want %#v", test.action, got, test.want)
		}
	}
}
