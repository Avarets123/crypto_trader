# Стратегии автотрейдинга

## Выбранные стратегии

- **Стратегия 1** — Межбиржевый арбитраж
- **Стратегия 2** — Pump Momentum Trading
- **Стратегия 8** — Volume Spike Detection

---

## Стратегия 1: Межбиржевый арбитраж

### Идея
Одна и та же монета стоит по-разному на разных биржах. Купить дешевле — продать дороже.

### Два вида арбитража

**A) Трансферный (реальный)**
```
Binance: BTC = $95,000
Bybit:   BTC = $95,200

Купить на Binance → вывести → продать на Bybit → профит $200
```
Проблема: вывод занимает 10-60 минут. За это время спред исчезнет.

**B) Виртуальный (без трансфера) — реалистичнее**
```
Держишь баланс на обеих биржах заранее:
  Binance: 1 BTC + $0 USDT
  Bybit:   0 BTC + $95,000 USDT

Спред открылся: Binance $95,000, Bybit $95,200
→ Продать BTC на Bybit за $95,200
→ Купить BTC на Binance за $95,000
→ Балансы выровнялись, заработали $200
```

### Что уже есть в коде
`comparator` уже делает всё нужное:
```
SpreadEvent {
    Symbol:       "BTCUSDT"
    ExchangeHigh: "bybit"    ← продавать здесь
    ExchangeLow:  "binance"  ← покупать здесь
    MaxSpreadPct: 0.21%
    PriceHigh:    95200
    PriceLow:     95000
}
```

### Что нужно добавить
```
REST клиент Binance  ─┐
REST клиент Bybit    ─┤→ ArbExecutor → PositionTracker → TG-уведомление
REST клиент OKX      ─┘
```

### Главные риски
| Риск | Описание |
|---|---|
| Исполнение | Пока отправляешь ордер — цена ушла |
| Комиссии | Спред должен быть > 2×taker fee (обычно 0.1%) |
| Проскальзывание | Крупный ордер сдвигает цену |
| Дисбаланс | На одной бирже закончились средства |

### Минимальный спред для профита
```
Binance taker fee: 0.10%
Bybit taker fee:   0.10%
Итого: 0.20%

Текущий порог comparator: 1.0% (задан в .env)
→ Уже фильтруем только выгодные ситуации
```

---

## Стратегия 2: Pump Momentum Trading

### Идея
Когда детектор фиксирует памп — цена уже начала двигаться. Запрыгнуть в движение и взять часть роста, выйти по TP или SL.

### Жизненный цикл сделки
```
Сигнал: SOLUSDT +3.5% за 60 сек на Bybit
         ↓
Market Buy (входим по рынку)
         ↓
Выставить два ордера:
  Take Profit: +2% от цены входа  (Limit Sell)
  Stop Loss:   -1% от цены входа  (Stop Market Sell)
         ↓
Ждём исполнения одного из них
         ↓
Закрыть оставшийся ордер
```

### Параметры стратегии
```
PUMP_ENTRY_THRESHOLD_PCT   = 3.0   // минимальный памп для входа (уже есть в .env)
PUMP_TAKE_PROFIT_PCT       = 2.0   // цель по прибыли
PUMP_STOP_LOSS_PCT         = 1.0   // максимальный убыток
PUMP_POSITION_SIZE_USDT    = 100   // размер позиции в USDT
PUMP_COOLDOWN_SEC          = 300   // не входить в один символ чаще чем раз в 5 мин
```

### Что уже есть
```go
det.WithOnPumpEvent(tgAgg.OnPumpEvent)
// Нужно добавить:
det.WithOnPumpEvent(pumpTrader.OnSignal)
```

### Главные риски
| Риск | Описание |
|---|---|
| Ловля хвоста | Памп уже на 5% — покупаешь на вершине |
| Fake pump | Манипуляция: быстрый рост и такой же быстрый откат |
| Малая ликвидность | Альткоины с маленьким объёмом — большое проскальзывание |

### Фильтры для снижения риска
```
1. Volume filter:  входить только если Volume24h > $1M
2. Spread filter:  bid/ask спред < 0.1% (есть ликвидность)
3. Cooldown:       не торговать монету повторно N минут
4. Daily loss cap: остановить торговлю если убыток дня > X USDT
```

---

## Стратегия 8: Volume Spike Detection

### Идея
Аномальный объём торгов — это след крупного игрока. Если объём резко вырос, а цена почти не изменилась — идёт тихое накопление. После него часто следует движение.

### Сигнал
```
Обычный объём ETHUSDT за 24ч: ~500M USDT
Сегодня объём: 1,500M USDT (+200%)
Изменение цены за тот же период: +0.3% (незначительное)

→ Сигнал: кто-то крупный накапливает позицию
→ Ожидаем движение вверх
```

### Два типа спайков
```
Тип A: Volume ↑↑ + Price ≈ (боковик)  → накопление → ожидаем рост
Тип B: Volume ↑↑ + Price ↓↓           → распродажа → ожидаем продолжение падения
Тип C: Volume ↑↑ + Price ↑↑           → это уже памп (Стратегия 2)
```

### Как считать "аномальность"
```sql
avg_volume = SELECT AVG(volume_24h) FROM tickers
             WHERE symbol = 'ETHUSDT' AND exchange = 'binance'
             AND created_at > NOW() - INTERVAL '7 days'

spike_ratio = current_volume / avg_volume

Порог: spike_ratio > 3.0 → сигнал
```

### Архитектура нового компонента
```
VolumeDetector
  ├── Получает тикеры через WithOnSend (как comparator и detector)
  ├── Хранит скользящее среднее volume per symbol
  ├── При spike_ratio > threshold → VolumeEvent
  └── Хуки: WithOnVolumeSpike(fn)
```

### Параметры
```
VOLUME_SPIKE_RATIO    = 3.0   // объём в N раз выше среднего
VOLUME_WINDOW_DAYS    = 7     // за сколько дней считать среднее
VOLUME_MIN_USDT       = 1M    // игнорировать монеты с малым объёмом
```

### Структура нового пакета
```
internal/shared/volumedetector/
  ├── detector.go   // логика расчёта среднего и детекции
  ├── event.go      // VolumeEvent struct
  └── config.go     // VOLUME_SPIKE_RATIO, VOLUME_WINDOW_DAYS
```

---

## Общий фундамент (нужен для всех трёх)

```
REST-клиент (Bybit testnet)
  └── PlaceOrder(symbol, side, type, qty, price)
  └── CancelOrder(orderID)
  └── GetBalance()

OrderManager
  └── OpenPosition(signal)
  └── ClosePosition(id)
  └── GetOpenPositions()

RiskManager
  └── CanTrade() bool          // дневной лимит, макс. позиций
  └── CalcPositionSize() float // размер позиции от баланса

PaperTrader (без реальных денег)
  └── Симулирует исполнение по ценам из тикеров
```

## Порядок реализации

1. `PaperTrader` — тестовый движок без реальных денег
2. `VolumeDetector` (стратегия 8) — только сигналы, без торговли
3. `PumpTrader` (стратегия 2) — торговля на paper mode
4. `ArbExecutor` (стратегия 1) — после того как всё остальное работает
