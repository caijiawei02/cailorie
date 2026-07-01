package llm

import (
	"regexp"
	"strconv"
	"strings"
)

var numberRe = regexp.MustCompile(`\d+`)

// ParseCalorieResponse interprets a raw text response from any LLM and
// returns the calorie integer, or one of the sentinel errors
// (ErrNotFood) when the response is not a usable calorie count.
func ParseCalorieResponse(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "NA") {
		return 0, ErrNotFood
	}
	// Tolerate responses like "450 calories" — extract first integer.
	m := numberRe.FindString(raw)
	if m == "" {
		return 0, ErrNotFood
	}
	n, err := strconv.Atoi(m)
	if err != nil || n <= 0 {
		return 0, ErrNotFood
	}
	return n, nil
}