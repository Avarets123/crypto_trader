package ticker

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const chanBuffer = 4192

type TickerService struct {
	repo     *TickerRepository
	log      *zap.Logger
	ch       chan Ticker
	size     int
	interval time.Duration
	onSend   func(Ticker)
}

func NewService(ctx context.Context, repo *TickerRepository, log *zap.Logger, cfg Config) *TickerService {
	service:= &TickerService{
		repo:     repo,
		log:      log,
		ch:       make(chan Ticker, chanBuffer),
		size:     cfg.BatchSize,
		interval: cfg.Interval,
	}

	go service.BatchWriter(ctx)


	return service
}

// WithOnSend устанавливает хук, вызываемый после каждого успешно отправленного тикера.
func (s *TickerService) WithOnSend(fn func(Ticker)) {
	s.onSend = fn
}

// Send отправляет тикер в канал. Неблокирующий: при переполнении — дропает.
func (s *TickerService) Send(t Ticker) {
	select {
	case s.ch <- t:
		if s.onSend != nil {
			s.onSend(t)
		}
	default:
		s.log.Warn("storage: channel full, ticker dropped", zap.String("symbol", t.Symbol))
	}
}

// Run запускает горутину батч-записи. Блокируется до ctx.Done().
func (s *TickerService) BatchWriter(ctx context.Context) {
	batch := make([]Ticker, 0, s.size)
	tick := time.NewTicker(s.interval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.repo.SaveBatch(ctx, batch); err != nil {
			s.log.Error("service: flush failed", zap.Error(err))
		}
		batch = batch[:0]
	}

	for {
		select {
		case t := <-s.ch:
			batch = append(batch, t)
			if len(batch) >= s.size {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case t := <-s.ch:
					batch = append(batch, t)
				default:
					flush()
					return
				}
			}
		}
	}
}
