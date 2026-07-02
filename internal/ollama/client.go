package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/caijiawei02/cailorie/internal/llm"
)

// Client calls an Ollama-compatible HTTP API (Ollama Cloud or a local Ollama
// server) for calorie estimation from food images.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// New constructs an Ollama client. baseURL is e.g. "https://api.ollama.com"
// (Ollama Cloud) or "http://localhost:11434" (local Ollama). apiKey is the
// Ollama Cloud bearer token (empty for local Ollama).
func New(baseURL, model, apiKey string) (*Client, error) {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		return nil, fmt.Errorf("OLLAMA_MODEL is required when LLM_PROVIDER=ollama")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Close is a no-op for the HTTP-based Ollama client (satisfies llm.Client).
func (c *Client) Close() error { return nil }

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   string        `json:"format,omitempty"`
}

type chatMessage struct {
	Role    string      `json:"role"`
	Content string      `json:"content"`
	Images  []string    `json:"images,omitempty"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error,omitempty"`
}

// EstimateCalories sends the image bytes to the Ollama model and returns the
// calorie estimate as an integer. Returns a sentinel llm.Err* on failure.
func (c *Client) EstimateCalories(ctx context.Context, imageBytes []byte, mimeType, userText string) (int, error) {
	b64 := base64.StdEncoding.EncodeToString(imageBytes)
	body := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{
				Role:    "user",
				Content: llm.CaloriePromptFor(userText),
				Images:  []string{b64},
			},
		},
		Stream: false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("ollama marshal: %w", err)
	}

	url := c.baseURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", llm.ErrProviderDown, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("ollama read body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusUnauthorized, http.StatusForbidden:
		return 0, fmt.Errorf("%w: %s", llm.ErrInvalidKey, string(respBytes))
	case http.StatusNotFound:
		return 0, fmt.Errorf("%w: model %q not found — pull it first with `ollama pull %s`", llm.ErrModelNotFound, c.model, c.model)
	case http.StatusTooManyRequests:
		return 0, fmt.Errorf("%w: %s", llm.ErrQuotaExceeded, string(respBytes))
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return 0, fmt.Errorf("%w: status %d: %s", llm.ErrProviderDown, resp.StatusCode, string(respBytes))
	default:
		return 0, fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, string(respBytes))
	}

	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return 0, fmt.Errorf("ollama unmarshal: %w", err)
	}
	if cr.Error != "" {
		return 0, fmt.Errorf("ollama error: %s", cr.Error)
	}

	raw := strings.TrimSpace(cr.Message.Content)
	log.Printf("ollama raw response: %q", raw)

	return llm.ParseCalorieResponse(raw)
}