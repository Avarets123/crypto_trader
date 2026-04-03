package ticker

import (
	"time"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

type Config struct {
	BatchSize int
	Interval  time.Duration
}

func LoadConfig() Config {
	size := sharedconfig.GetEnvInt("BATCH_SIZE", 1000)
	ms := sharedconfig.GetEnvInt("BATCH_INTERVAL_MS", 5500)
	return Config{
		BatchSize: size,
		Interval:  time.Duration(ms) * time.Millisecond,
	}
}
