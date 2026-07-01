package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/caijiawei02/cailorie/internal/llm"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Client wraps the Gemini generative model for calorie estimation.
type Client struct {
	model  *genai.GenerativeModel
	client *genai.Client
}

// New constructs a Gemini client using an API key.
func New(apiKey, model string) (*Client, error) {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("gemini new client: %w", err)
	}
	return &Client{client: c, model: c.GenerativeModel(model)}, nil
}

// Close releases the underlying client.
func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// EstimateCalories sends the image bytes to Gemini and returns the calorie
// estimate as an integer. Returns a sentinel llm.Err* on failure.
func (c *Client) EstimateCalories(ctx context.Context, imageBytes []byte, mimeType string) (int, error) {
	img := genai.ImageData(mimeType, imageBytes)
	resp, err := c.model.GenerateContent(ctx, img, genai.Text(llm.CaloriePrompt))
	if err != nil {
		return 0, classifyAPIError(err)
	}
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		if resp != nil && resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != 0 {
			return 0, llm.ErrSafetyBlocked
		}
		return 0, llm.ErrNotFood
	}

	var sb strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		if t, ok := p.(genai.Text); ok {
			sb.WriteString(string(t))
		}
	}
	raw := strings.TrimSpace(sb.String())
	log.Printf("gemini raw response: %q", raw)

	return llm.ParseCalorieResponse(raw)
}

// classifyAPIError converts a raw Gemini/Google API error into a typed
// sentinel so the handler can give the user a specific message.
func classifyAPIError(err error) error {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 401, 403:
			return fmt.Errorf("%w: %s", llm.ErrInvalidKey, gerr.Message)
		case 404:
			return fmt.Errorf("%w: %s", llm.ErrModelNotFound, gerr.Message)
		case 429:
			return fmt.Errorf("%w: %s", llm.ErrQuotaExceeded, gerr.Message)
		case 500, 502, 503:
			return fmt.Errorf("%w: %s", llm.ErrProviderDown, gerr.Message)
		}
	}
	return fmt.Errorf("gemini generate content: %w", err)
}