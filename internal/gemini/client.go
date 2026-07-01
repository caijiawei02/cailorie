package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Client wraps the Gemini generative model for calorie estimation.
type Client struct {
	model  *genai.GenerativeModel
	client *genai.Client
}

// New constructs a Gemini client using an API key.
func New(apiKey string) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("gemini new client: %w", err)
	}
	return &Client{client: c, model: c.GenerativeModel("gemini-1.5-flash")}, nil
}

// Close releases the underlying client.
func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

const caloriePrompt = "Identify the food in this image and estimate the total calories of the entire meal. Reply with ONLY a single integer (the calorie count). Do not include any text, units, or punctuation. If the image does not contain food or the food cannot be identified, reply with exactly: NA"

// ErrNotFood indicates Gemini could not identify food in the image.
var ErrNotFood = errors.New("image is not food or could not be identified")

var numberRe = regexp.MustCompile(`\d+`)

// EstimateCalories sends the image bytes to Gemini and returns the calorie
// estimate as an integer. Returns ErrNotFood if the image is not food or
// cannot be identified.
func (c *Client) EstimateCalories(ctx context.Context, imageBytes []byte, mimeType string) (int, error) {
	img := genai.ImageData(mimeType, imageBytes)
	resp, err := c.model.GenerateContent(ctx, img, genai.Text(caloriePrompt))
	if err != nil {
		return 0, fmt.Errorf("gemini generate content: %w", err)
	}
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return 0, ErrNotFood
	}

	var sb strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		if t, ok := p.(genai.Text); ok {
			sb.WriteString(string(t))
		}
	}
	raw := strings.TrimSpace(sb.String())
	log.Printf("gemini raw response: %q", raw)

	if strings.EqualFold(raw, "NA") || raw == "" {
		return 0, ErrNotFood
	}

	// Tolerate responses like "450 calories" — extract first integer.
	m := numberRe.FindString(raw)
	if m == "" {
		return 0, ErrNotFood
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0, ErrNotFood
	}
	if n <= 0 {
		return 0, ErrNotFood
	}
	return n, nil
}