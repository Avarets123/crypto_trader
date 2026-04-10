package telegram

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap/zapcore"
)

// ErrorCore — zap core, пересылающий сообщения уровня ERROR и выше в Telegram-топик.
type ErrorCore struct {
	notifier *Notifier
	threadID int
	ctx      context.Context
}

// NewErrorCore создаёт ErrorCore. Сообщения уровня ERROR и выше отправляются в threadID.
func NewErrorCore(ctx context.Context, notifier *Notifier, threadID int) *ErrorCore {
	return &ErrorCore{
		notifier: notifier,
		threadID: threadID,
		ctx:      ctx,
	}
}

func (c *ErrorCore) Enabled(lvl zapcore.Level) bool {
	return lvl >= zapcore.ErrorLevel
}

func (c *ErrorCore) With(fields []zapcore.Field) zapcore.Core {
	return c
}

func (c *ErrorCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

func (c *ErrorCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🚨 <b>%s</b>\n%s",
		entry.Level.CapitalString(),
		entry.Message,
	))

	for _, f := range fields {
		if f.Key == "error" || f.Key == "err" {
			if f.Interface != nil {
				sb.WriteString(fmt.Sprintf("\n<code>%v</code>", f.Interface))
			} else if f.String != "" {
				sb.WriteString(fmt.Sprintf("\n<code>%s</code>", f.String))
			}
		}
	}

	msg := sb.String()
	go c.notifier.SendToThread(c.ctx, msg, c.threadID)
	return nil
}

func (c *ErrorCore) Sync() error {
	return nil
}
