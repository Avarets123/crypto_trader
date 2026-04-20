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

// Summarize анализирует новость на предмет роста/падения криптовалют.
// Возвращает строку вида "UP:BTC,ETH", "DOWN:SOL", "UP:BTC|DOWN:ETH" или "NONE".
func (c *Client) Summarize(ctx context.Context, title, description string) (string, error) {
	prompt := fmt.Sprintf(
		"You are a crypto market signal detector.\n"+
			"Task: analyze the news and detect if it directly or indirectly signals price movement for any specific cryptocurrency.\n"+
			"Rules:\n"+
			"1. Use ticker symbols (BTC, ETH, SOL, etc.). Bitcoin→BTC, Ethereum→ETH, etc.\n"+
			"2. If news suggests price growth or bullish outlook for coins, output: UP:<TICKER1>,<TICKER2>\n"+
			"3. If news suggests price decline or bearish outlook for coins, output: DOWN:<TICKER1>,<TICKER2>\n"+
			"4. If both signals exist for different coins, output: UP:<TICKERS>|DOWN:<TICKERS>\n"+
			"5. If no clear price signal for any specific coin, output: NONE\n"+
			"6. Return ONLY the result string. No explanation, no punctuation, no extra text.\n\n"+
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
