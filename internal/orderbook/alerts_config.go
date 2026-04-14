package orderbook

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// AlertsConfig — конфигурация мониторинга объёма стакана.
type AlertsConfig struct {
	Enabled            bool
	VolumeChangePct    float64
	CheckIntervalSec   int
	RefreshIntervalMin int
	TradeWindowSec     int // размер скользящего окна для статистики сделок (сек)
}

// LoadAlertsConfig читает конфигурацию из env-переменных.
func LoadAlertsConfig() AlertsConfig {
	return AlertsConfig{
		Enabled:            sharedconfig.GetEnvBool("ORDERBOOK_ALERTS_ENABLED", true),
		VolumeChangePct:    sharedconfig.GetEnvFloat("ORDERBOOK_VOLUME_CHANGE_PCT", 20.0),
		CheckIntervalSec:   sharedconfig.GetEnvInt("ORDERBOOK_CHECK_INTERVAL_SEC", 30),
		RefreshIntervalMin: sharedconfig.GetEnvInt("ORDERBOOK_REFRESH_INTERVAL_MIN", 5),
		TradeWindowSec:     sharedconfig.GetEnvInt("ORDERBOOK_TRADE_WINDOW_SEC", 300),
	}
}
