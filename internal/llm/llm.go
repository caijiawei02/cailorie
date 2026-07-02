package llm

import (
	"context"
	"errors"
	"strings"
)

// Client abstracts a vision-capable LLM that estimates calories from a food
// image. Implementations: gemini.Client, ollama.Client. userText is the
// optional caption the user typed alongside the photo; pass "" when absent.
type Client interface {
	EstimateCalories(ctx context.Context, imageBytes []byte, mimeType, userText string) (int, error)
	Close() error
}

// Sentinel errors shared across providers. Providers wrap these via
// fmt.Errorf("%w: ...", ErrX) so callers can use errors.Is.
var (
	ErrNotFood       = errors.New("image is not food or could not be identified")
	ErrQuotaExceeded = errors.New("quota exceeded")
	ErrInvalidKey    = errors.New("api key is invalid or unauthorized")
	ErrModelNotFound = errors.New("model not found or unavailable")
	ErrSafetyBlocked = errors.New("response blocked for safety reasons")
	ErrProviderDown  = errors.New("provider is unreachable or returned a server error")
)

// baseCaloriePrompt is the instruction sent to every vision LLM. Providers wrap
// the image + this prompt in their own request format.
const baseCaloriePrompt = "Identify the food in this image and estimate the total calories of the entire meal. Reply with ONLY a single integer (the calorie count). Do not include any text, units, or punctuation. If the image does not contain food or the food cannot be identified, reply with exactly: NA"

// CaloriePromptFor builds the prompt sent to the vision LLM. When userText is
// non-empty (the user typed a caption alongside their photo, e.g.
// "Spicy Ramen with chicken cutlet"), it is appended so the model can use the
// user's description to guide its estimate. When userText is empty, the bare
// base prompt is returned, preserving existing behaviour for photo-only
// messages.
func CaloriePromptFor(userText string) string {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return baseCaloriePrompt
	}
	return baseCaloriePrompt + "\n\nThe user described this meal as: " + userText + ". Use this description to guide your estimate."
}