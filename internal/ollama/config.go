package ollama

import "github.com/osman/bot-traider/internal/shared/config"

// Config — настройки клиента Ollama.
type Config struct {
	URL   string
	Model string
}

// LoadConfig загружает конфигурацию из переменных окружения.
func LoadConfig() Config {
	return Config{
		URL:   config.GetEnv("OLLAMA_URL", "http://localhost:11434"),
		Model: config.GetEnv("OLLAMA_MODEL", "llama3"),
	}
}
