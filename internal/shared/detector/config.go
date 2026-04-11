package detector

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config задаёт параметры детектора ценовых аномалий.
type Config struct {
	PumpThresholdPct  float64 // PUMP_THRESHOLD_PCT, по умолчанию 3.0
	CrashThresholdPct float64 // CRASH_THRESHOLD_PCT, по умолчанию 3.0
	WindowSec         int     // DETECTOR_WINDOW_SEC, по умолчанию 60

	// Volume Spike Detection
	VolumeSpikeRatio    float64 // VOLUME_SPIKE_RATIO, по умолчанию 3.0
	VolumeWindowSize    int     // VOLUME_WINDOW_SIZE, по умолчанию 100
	VolumeMinUSDT       float64 // VOLUME_MIN_USDT, по умолчанию 1_000_000
	VolumePriceChangePct float64 // VOLUME_PRICE_CHANGE_PCT, по умолчанию 1.0
}

// LoadConfig читает конфигурацию детектора из переменных окружения.
func LoadConfig() Config {
	return Config{
		PumpThresholdPct:    sharedconfig.GetEnvFloat("PUMP_THRESHOLD_PCT", 33.0),
		CrashThresholdPct:   sharedconfig.GetEnvFloat("CRASH_THRESHOLD_PCT", 33.0),
		WindowSec:           sharedconfig.GetEnvInt("DETECTOR_WINDOW_SEC", 60),
		VolumeSpikeRatio:    sharedconfig.GetEnvFloat("VOLUME_SPIKE_RATIO", 3.0),
		VolumeWindowSize:    sharedconfig.GetEnvInt("VOLUME_WINDOW_SIZE", 100),
		VolumeMinUSDT:       sharedconfig.GetEnvFloat("VOLUME_MIN_USDT", 1_000_000),
		VolumePriceChangePct: sharedconfig.GetEnvFloat("VOLUME_PRICE_CHANGE_PCT", 1.0),
	}
}
