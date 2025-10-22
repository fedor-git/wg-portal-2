package main
package main

import (
	"fmt"
	"time"
)

func main() {
	// Тест парсингу різних форматів TTL
	testCases := []string{
		"1h",
		"24h", 
		"2d",
		"7d",
		"30d",
		"168h", // 7 днів в годинах
		"720h", // 30 днів в годинах
	}
	
	for _, ttlStr := range testCases {
		if ttlDuration, err := time.ParseDuration(ttlStr); err == nil {
			currentDate := time.Now()
			expiryDate := currentDate.Add(ttlDuration)
			rfc3339Format := expiryDate.Format(time.RFC3339)
			fmt.Printf("TTL: %s -> Duration: %v -> Expiry: %s\n", ttlStr, ttlDuration, rfc3339Format)
		} else {
			fmt.Printf("TTL: %s -> Error: %v\n", ttlStr, err)
		}
	}
	
	// Тест зворотного парсингу RFC3339
	fmt.Println("\nТест парсингу RFC3339:")
	testTimestamp := "2025-09-18T15:04:05Z"
	if parsedTime, err := time.Parse(time.RFC3339, testTimestamp); err == nil {
		fmt.Printf("Timestamp: %s -> Parsed: %v\n", testTimestamp, parsedTime)
	} else {
		fmt.Printf("Timestamp: %s -> Error: %v\n", testTimestamp, err)
	}
}