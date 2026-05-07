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

// AnalyzeListing анализирует объявление о новом листинге и возвращает краткий
// текстовый вывод на русском языке (2-3 предложения): что за токен и чего ожидать.
func (c *Client) AnalyzeListing(ctx context.Context, title, description string) (string, error) {
	prompt := fmt.Sprintf(
		"You are a crypto market analyst. A new token listing has been announced on an exchange.\n"+
			"Write a brief analysis in Russian (1-2 sentences):\n"+
			"1. What is this token or project?\n"+
			"2. What price action can be expected after the listing?\n"+
			"Return ONLY the analysis text in Russian. No headers, no bullet points, no extra words.\n\n"+
			"Title: %s\nDescription: %s",
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

// Summarize анализирует новость на предмет роста/падения криптовалют.
// Возвращает строку вида "UP:BTC:high,ETH:medium", "DOWN:SOL:low",
// "UP:BTC:high|DOWN:ETH:medium" или "NONE".
//
// CONFIDENCE — уверенность в сигнале:
//   - high   — официальное объявление с конкретикой (партнёрство, листинг, интеграция);
//   - medium — позитивная/негативная новость, но реализация под вопросом или спекулятивно;
//   - low    — мелкие новости, слухи, мнения, общая аналитика.
//
// Стратегии торгуют только high; medium/low идут в Telegram, но без сделки.
func (c *Client) Summarize(ctx context.Context, title, description string) (string, error) {
	prompt := fmt.Sprintf(
		"You are a crypto market signal detector.\n"+
			"Task: analyze the news and detect if it directly or indirectly signals price movement for any specific cryptocurrency, AND classify your confidence in the signal.\n"+
			"\n"+
			"Output format (STRICT):\n"+
			"- Bullish: UP:<TICKER1>:<CONFIDENCE>,<TICKER2>:<CONFIDENCE>\n"+
			"- Bearish: DOWN:<TICKER1>:<CONFIDENCE>,<TICKER2>:<CONFIDENCE>\n"+
			"- Mixed:   UP:<TICKER>:<CONFIDENCE>|DOWN:<TICKER>:<CONFIDENCE>\n"+
			"- No signal: NONE\n"+
			"\n"+
			"CONFIDENCE values (lowercase, REQUIRED for every ticker):\n"+
			"- high:   official announcement from a major source about a concrete event — partnership, exchange listing, mainnet launch, ETF approval, large integration, regulatory decision with named ticker.\n"+
			"- medium: positive/negative news with potential price impact, but execution is uncertain, time horizon unclear, or details speculative.\n"+
			"- low:    minor news, rumors, opinions, general market commentary, price predictions without concrete catalyst.\n"+
			"\n"+
			"Ticker rules:\n"+
			"- Use uppercase ticker symbols (BTC, ETH, SOL, BNB, XRP, etc.). Bitcoin→BTC, Ethereum→ETH, Solana→SOL.\n"+
			"- Only include tickers that are clearly named or unambiguously implied. Do NOT invent or infer tickers from generic phrases.\n"+
			"- If news is general market commentary without a specific coin, return NONE.\n"+
			"\n"+
			"Output discipline:\n"+
			"- Return ONLY the result string. No explanation, no punctuation outside the format, no extra text, no quotes, no markdown.\n"+
			"- Confidence is MANDATORY for each ticker. Format strictly as TICKER:CONFIDENCE.\n"+
			"\n"+
			"Examples:\n"+
			"- \"Visa partners with Solana for stablecoin payments\" → UP:SOL:high\n"+
			"- \"Analyst predicts Bitcoin could reach $200k\" → UP:BTC:low\n"+
			"- \"SEC sues Ripple over XRP sales\" → DOWN:XRP:high\n"+
			"- \"Ethereum upgrade may cause short-term volatility\" → UP:ETH:medium\n"+
			"- \"Crypto market sees $50M outflow today\" → NONE\n"+
			"\n"+
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
