package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewDomainPeer_ExpiresAtParsing(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt string
		expectNil bool
	}{
		{
			name:      "valid RFC3339 format",
			expiresAt: "2025-10-24T01:00:00.000Z",
			expectNil: false,
		},
		{
			name:      "problematic case from error",
			expiresAt: "\"2025-10-23T20:22:00.000Z\"",
			expectNil: false,
		},
		{
			name:      "double quoted case",
			expiresAt: "\"\"2025-10-24T01:00:00.000Z\"\"",
			expectNil: false,
		},
		{
			name:      "valid RFC3339 format with quotes",
			expiresAt: "\"2025-10-24T01:00:00.000Z\"",
			expectNil: false,
		},
		{
			name:      "empty string",
			expiresAt: "",
			expectNil: true,
		},
		{
			name:      "invalid format",
			expiresAt: "2025-10-24",
			expectNil: true,
		},
		{
			name:      "valid RFC3339 with timezone",
			expiresAt: "2025-10-24T01:00:00+02:00",
			expectNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := &Peer{
				Identifier:          "test-peer",
				UserIdentifier:      "test-user",
				InterfaceIdentifier: "test-interface",
				ExpiresAt:           tt.expiresAt,
				Addresses:           []string{"10.0.0.1/32"},
			}

			domainPeer := NewDomainPeer(peer)

			if tt.expectNil {
				assert.Nil(t, domainPeer.ExpiresAt, "ExpiresAt should be nil for input: %s", tt.expiresAt)
			} else {
				assert.NotNil(t, domainPeer.ExpiresAt, "ExpiresAt should not be nil for input: %s", tt.expiresAt)
				// Check that the parsed time is reasonable
				if tt.expiresAt == "2025-10-24T01:00:00+02:00" {
					// This time is actually 23:00 UTC the previous day
					expectedTime := time.Date(2025, 10, 23, 23, 0, 0, 0, time.UTC)
					assert.True(t, domainPeer.ExpiresAt.Equal(expectedTime),
						"Parsed time should be %v for input: %s, got: %v", expectedTime, tt.expiresAt, domainPeer.ExpiresAt)
				} else if tt.expiresAt == "\"2025-10-23T20:22:00.000Z\"" {
					// Problematic case from the error
					expectedTime := time.Date(2025, 10, 23, 20, 22, 0, 0, time.UTC)
					assert.True(t, domainPeer.ExpiresAt.Equal(expectedTime),
						"Parsed time should be %v for input: %s, got: %v", expectedTime, tt.expiresAt, domainPeer.ExpiresAt)
				} else {
					// For all other cases, just verify it's a valid time 
					assert.True(t, domainPeer.ExpiresAt.Year() == 2025,
						"Parsed time should have year 2025 for input: %s, got: %v", tt.expiresAt, domainPeer.ExpiresAt)
				}
			}
		})
	}
}