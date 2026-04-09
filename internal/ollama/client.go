package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const requestTimeout = 3000 * time.Second

// Client — HTTP-клиент для локального Ollama-сервера.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// NewClient создаёт Client с заданной конфигурацией.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type generateResponse struct {
	Response string `json:"response"`
}

// Summarize переводит заголовок + описание новости на русский язык,
// возвращает одно предложение с хештегами упомянутых криптовалют в начале.
func (c *Client) Summarize(ctx context.Context, title, description string) (string, error) {
	prompt := fmt.Sprintf(
		"You are a crypto news summarizer.\n"+
			"Task:\n"+
			"1. Identify all specific cryptocurrencies mentioned (e.g. Bitcoin→#BTC, Ethereum→#ETH, Solana→#SOL, etc.).\n"+
			"2. If any cryptocurrencies are found, start the output with their hashtags separated by spaces (e.g. \"#BTC #ETH \").\n"+
			"3. Translate the news to Russian and summarize in exactly ONE sentence.\n"+
			"4. Return ONLY the result: optional hashtags followed by the single Russian sentence. No extra text, no labels, no quotes.\n\n"+
			"Title: %s\nContent: %s",
		title, description,
	)

	body, err := json.Marshal(generateRequest{
		Model:  c.cfg.Model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: unexpected status %d", resp.StatusCode)
	}

	var result generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama: decode response: %w", err)
	}

	summary := strings.TrimSpace(result.Response)
	if summary == "" {
		return "", fmt.Errorf("ollama: empty response")
	}
	return summary, nil
}
