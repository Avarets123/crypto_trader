package tinkoff

import "go.uber.org/zap"

// zapLogger адаптирует *zap.Logger к интерфейсу investgo.Logger.
type zapLogger struct {
	log *zap.Logger
}

func (l *zapLogger) Infof(template string, args ...any) {
	l.log.Sugar().Infof(template, args...)
}

func (l *zapLogger) Errorf(template string, args ...any) {
	l.log.Sugar().Errorf(template, args...)
}

func (l *zapLogger) Fatalf(template string, args ...any) {
	l.log.Sugar().Fatalf(template, args...)
}
