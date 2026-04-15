package orderbook

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/shared/utils"
	"go.uber.org/zap"
)

// LocalBook — полный стакан одного символа в памяти.
type LocalBook struct {
	mu           sync.RWMutex
	bids         map[string]string // price → qty
	asks         map[string]string
	lastUpdateID int64
}

func newLocalBook() *LocalBook {
	return &LocalBook{
		bids: make(map[string]string),
		asks: make(map[string]string),
	}
}

// Init инициализирует стакан из REST-снимка.
func (lb *LocalBook) Init(bids, asks [][2]string, lastUpdateID int64, log *zap.Logger) {
	defer utils.TimeTracker(log, "Init local_book")()

	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.bids = make(map[string]string, len(bids))
	lb.asks = make(map[string]string, len(asks))
	for _, b := range bids {
		lb.bids[b[0]] = b[1]
	}
	for _, a := range asks {
		lb.asks[a[0]] = a[1]
	}
	lb.lastUpdateID = lastUpdateID
}

// LastUpdateID возвращает текущий lastUpdateID.
func (lb *LocalBook) LastUpdateID() int64 {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.lastUpdateID
}

// Apply применяет инкрементальное обновление (diff) стакана.
// Если finalID <= текущего lastUpdateID — обновление устарело и игнорируется.
func (lb *LocalBook) Apply(finalID int64, bids, asks [][]json.RawMessage) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if finalID <= lb.lastUpdateID {
		return
	}
	applyEntries(lb.bids, bids)
	applyEntries(lb.asks, asks)
	lb.lastUpdateID = finalID
}

// applyEntries применяет список изменений к map price→qty.
// Если qty == 0 — уровень удаляется.
func applyEntries(m map[string]string, rows [][]json.RawMessage) {
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		var price, qty string
		json.Unmarshal(row[0], &price) //nolint:errcheck
		json.Unmarshal(row[1], &qty)   //nolint:errcheck
		f, _ := strconv.ParseFloat(qty, 64)
		if f == 0 {
			delete(m, price)
		} else {
			m[price] = qty
		}
	}
}

// Snapshot возвращает отсортированный снимок стакана.
// Bids — по убыванию цены, Asks — по возрастанию.
func (lb *LocalBook) Snapshot(log *zap.Logger,symbol string) OrderBook {
	defer utils.TimeTracker(log, "Snapshot local_book")()

	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return OrderBook{
		Symbol:    symbol,
		Bids:      sortedEntries(lb.bids, true),
		Asks:      sortedEntries(lb.asks, false),
		UpdatedAt: time.Now(),
	}
}

func sortedEntries(m map[string]string, desc bool) []Entry {

	type kv struct {
		price    float64
		priceStr string
		qty      string
	}
	entries := make([]kv, 0, len(m))
	for p, q := range m {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			continue
		}
		entries = append(entries, kv{f, p, q})
	}
	if desc {
		sort.Slice(entries, func(i, j int) bool { return entries[i].price > entries[j].price })
	} else {
		sort.Slice(entries, func(i, j int) bool { return entries[i].price < entries[j].price })
	}
	result := make([]Entry, len(entries))
	for i, e := range entries {
		result[i] = Entry{Price: e.priceStr, Qty: e.qty}
	}
	return result
}

// Store — хранилище локальных стаканов по символам.
type Store struct {
	mu    sync.RWMutex
	books map[string]*LocalBook
}

// NewStore создаёт пустой Store.
func NewStore() *Store {
	return &Store{books: make(map[string]*LocalBook)}
}

// GetOrCreate возвращает существующий или новый LocalBook для символа.
func (s *Store) GetOrCreate(symbol string) *LocalBook {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lb, ok := s.books[symbol]; ok {
		return lb
	}
	lb := newLocalBook()
	s.books[symbol] = lb
	return lb
}

// Snapshot возвращает снимок стакана по символу.
// Возвращает false если символ ещё не инициализирован.
func (s *Store) Snapshot(symbol string, log *zap.Logger) (*OrderBook, bool) {
	s.mu.RLock()
	lb, ok := s.books[symbol]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	ob := lb.Snapshot(log,symbol)
	return &ob, true
}

// Remove удаляет LocalBook символа из Store.
func (s *Store) Remove(symbol string) {
	s.mu.Lock()
	delete(s.books, symbol)
	s.mu.Unlock()
}
