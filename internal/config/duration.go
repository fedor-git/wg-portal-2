package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDurationWithDays парсить duration з підтримкою днів (d) та інших одиниць часу
func ParseDurationWithDays(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	
	// Якщо закінчується на 'd', обробляємо як дні
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, fmt.Errorf("invalid days format: %w", err)
		}
		// Не дозволяємо негативні значення для TTL
		if days < 0 {
			return 0, fmt.Errorf("negative days are not allowed: %d", days)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	
	// Інакше використовуємо стандартний парсер
	duration, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	
	// Не дозволяємо негативні тривалості для TTL
	if duration < 0 {
		return 0, fmt.Errorf("negative duration is not allowed: %v", duration)
	}
	
	return duration, nil
}