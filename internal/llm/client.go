package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type ClientConfig struct {
	APIKey     string
	Model      string
	Timeout    time.Duration
	RetryCount int
	RetryDelay time.Duration
	MaxRPM     int
}

type Client struct {
	client  *openai.Client
	cfg     ClientConfig
	limiter *rateLimiter
}

func NewClient(cfg ClientConfig) *Client {
	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
	)
	return &Client{
		client:  &client,
		cfg:     cfg,
		limiter: newRateLimiter(cfg.MaxRPM),
	}
}

// CallPlain sends a plain chat completion (no JSON schema enforcement) and returns the text response.
func (c *Client) CallPlain(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	if !c.limiter.Allow() {
		return "", errors.New("rate limit exceeded")
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	resp, err := c.client.Chat.Completions.New(callCtx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(c.cfg.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMessage),
		},
	})
	if err != nil && c.cfg.RetryCount > 0 {
		retryCtx, retryCancel := context.WithTimeout(ctx, c.cfg.Timeout/2)
		defer retryCancel()
		resp, err = c.client.Chat.Completions.New(retryCtx, openai.ChatCompletionNewParams{
			Model: openai.ChatModel(c.cfg.Model),
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userMessage),
			},
		})
	}
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("empty response from LLM")
	}
	return resp.Choices[0].Message.Content, nil
}

// Call sends a structured chat completion and returns the raw JSON response string.
func (c *Client) Call(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	if !c.limiter.Allow() {
		return "", errors.New("rate limit exceeded")
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	result, err := c.doCall(callCtx, systemPrompt, userMessage)
	if err != nil && c.cfg.RetryCount > 0 {
		// one retry with a shorter timeout
		retryCtx, retryCancel := context.WithTimeout(ctx, c.cfg.Timeout/2)
		defer retryCancel()
		result, err = c.doCall(retryCtx, systemPrompt, userMessage)
	}
	return result, err
}

func (c *Client) doCall(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	schemaBytes, _ := json.Marshal(tradeDecisionSchema())

	resp, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(c.cfg.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMessage),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "trade_decision",
					Strict: openai.Bool(true),
					Schema: schemaBytes,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("empty response from LLM")
	}
	return resp.Choices[0].Message.Content, nil
}

// tradeDecisionSchema returns the JSON schema for structured output enforcement.
func tradeDecisionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"OPEN_SHORT", "SKIP"},
			},
			"symbol": map[string]any{
				"type":        "string",
				"description": "Selected candidate symbol. Empty string if SKIP.",
			},
			"confidence": map[string]any{
				"type":        "integer",
				"description": "Confidence score 0-100. Must be >= 60 for OPEN_SHORT.",
			},
			"entry_mode": map[string]any{
				"type": "string",
				"enum": []string{"FRONTRUN", "LAST_MINUTE", "AFTER"},
			},
			"entry_reasons": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "2-4 concise reasons for the decision",
			},
			"warnings": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "0-3 risk warnings",
			},
			"skip_reason": map[string]any{
				"type":        "string",
				"description": "Reason for skipping. Empty string if OPEN_SHORT.",
			},
		},
		"required":             []string{"action", "symbol", "confidence", "entry_mode", "entry_reasons", "warnings", "skip_reason"},
		"additionalProperties": false,
	}
}

// rateLimiter is a simple token bucket limited to MaxRPM requests per minute.
type rateLimiter struct {
	mu       sync.Mutex
	tokens   int
	maxRPM   int
	lastFill time.Time
}

func newRateLimiter(maxRPM int) *rateLimiter {
	return &rateLimiter{
		tokens:   maxRPM,
		maxRPM:   maxRPM,
		lastFill: time.Now(),
	}
}

func (r *rateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastFill)
	refill := int(elapsed.Minutes() * float64(r.maxRPM))
	if refill > 0 {
		r.tokens += refill
		if r.tokens > r.maxRPM {
			r.tokens = r.maxRPM
		}
		r.lastFill = now
	}

	if r.tokens <= 0 {
		return false
	}
	r.tokens--
	return true
}
