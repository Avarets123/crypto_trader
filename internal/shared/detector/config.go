package detector

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config задаёт параметры детектора ценовых аномалий.
type Config struct {
	PumpThresholdPct  float64 // PUMP_THRESHOLD_PCT, по умолчанию 3.0
	CrashThresholdPct float64 // CRASH_THRESHOLD_PCT, по умолчанию 3.0
	WindowSec         int     // DETECTOR_WINDOW_SEC, по умолчанию 60
}

// LoadConfig читает конфигурацию детектора из переменных окружения.
func LoadConfig() Config {
	return Config{
		PumpThresholdPct:  sharedconfig.GetEnvFloat("PUMP_THRESHOLD_PCT", 3.0),
		CrashThresholdPct: sharedconfig.GetEnvFloat("CRASH_THRESHOLD_PCT", 3.0),
		WindowSec:         sharedconfig.GetEnvInt("DETECTOR_WINDOW_SEC", 60),
	}
}
