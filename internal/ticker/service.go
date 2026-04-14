package ticker

import (
	"context"
	"time"

	"github.com/osman/bot-traider/internal/shared/utils"
	"go.uber.org/zap"
)

const chanBuffer = 4192

type TickerService struct {
	repo     *TickerRepository
	log      *zap.Logger
	ch       chan Ticker
	size     int
	interval time.Duration
	onSend   []func(Ticker)
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

// WithOnSend добавляет хук, вызываемый при каждом тикере (независимо от состояния канала хранилища).
func (s *TickerService) WithOnSend(fn func(Ticker)) {
	s.onSend = append(s.onSend, fn)
}

// Send вызывает все onSend-хуки (comparator, detector, arb) всегда,
// затем неблокирующе пишет в канал хранилища — при переполнении дропает только запись в БД.
func (s *TickerService) Send(t Ticker) {
	defer utils.TimeTracker(s.log, "Send; exchange_orders fetcher")()

	for _, fn := range s.onSend {
		fn(t)
	}
	select {
	case s.ch <- t:
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
