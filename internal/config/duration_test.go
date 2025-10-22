package config

import (
	"testing"
	"time"
)

func TestParseDurationWithDays(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		hasError bool
	}{
		{"", 0, false},
		{"1h", 1 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"2d", 48 * time.Hour, false},
		{"7d", 168 * time.Hour, false},
		{"30d", 720 * time.Hour, false},
		{"1m", 1 * time.Minute, false},
		{"30s", 30 * time.Second, false},
		{"invalid", 0, true},
		{"5x", 0, true},
		{"abc", 0, true},
		{"-1d", 0, true}, // negative days should be rejected
		{"-1h", 0, true}, // negative hours should be rejected
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result, err := ParseDurationWithDays(test.input)
			
			if test.hasError {
				if err == nil {
					t.Errorf("Expected error for input %q, but got none", test.input)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for input %q: %v", test.input, err)
				}
				if result != test.expected {
					t.Errorf("For input %q, expected %v, got %v", test.input, test.expected, result)
				}
			}
		})
	}
}