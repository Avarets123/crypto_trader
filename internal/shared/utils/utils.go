package utils

import (
	"time"

	"go.uber.org/zap"
)


func TimeTracker(log *zap.Logger, fnName string, mustLogged... bool) func() {
	start:= time.Now()
	return func() {

		since := time.Since(start)
		sm := since.Milliseconds()

		
		if len(mustLogged) > 0 && mustLogged[0] {
			log.Debug("Performance", zap.String("func", fnName), zap.String("take", since.String()) )
			return
		}
		

		if sm < 100 {
			return
		}

		if sm < 500 {
			log.Debug("Performance", zap.String("func", fnName), zap.String("take", since.String()) )
			return
		} else {
			log.Warn("Performance", zap.String("func", fnName), zap.String("take", since.String()) )
			return
		}
	}
}