package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseDurationWithDays_TTLManager(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		hasError bool
	}{
		{
			name:     "24 hours for peer TTL",
			input:    "24h",
			expected: 24 * time.Hour,
			hasError: false,
		},
		{
			name:     "1 day for peer TTL",
			input:    "1d",
			expected: 24 * time.Hour,
			hasError: false,
		},
		{
			name:     "2 days for peer TTL",
			input:    "2d",
			expected: 48 * time.Hour,
			hasError: false,
		},
		{
			name:     "1 hour for peer TTL",
			input:    "1h",
			expected: 1 * time.Hour,
			hasError: false,
		},
		{
			name:     "30 minutes for peer TTL",
			input:    "30m",
			expected: 30 * time.Minute,
			hasError: false,
		},
		{
			name:     "invalid format",
			input:    "invalid",
			expected: 0,
			hasError: true,
		},
		{
			name:     "negative duration",
			input:    "-1h",
			expected: 0,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseDurationWithDays(tt.input)
			
			if tt.hasError {
				assert.Error(t, err)
				assert.Equal(t, time.Duration(0), result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}