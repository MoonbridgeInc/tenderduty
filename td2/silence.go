package tenderduty

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// maxSilenceMinutes caps how long a single /silence call may suppress alert
// dispatch for a chain, so a forgotten request can't gag alerting indefinitely.
const maxSilenceMinutes = 24 * 60

// silenceChain suppresses outbound alert notifications for the named chain until the
// returned time. Detection and dashboard state are unaffected — only dispatch to
// PagerDuty/Discord/Telegram/Slack/webhook is suppressed (see (*Config).alert).
func silenceChain(chain string, minutes int) (time.Time, error) {
	if minutes < 1 || minutes > maxSilenceMinutes {
		return time.Time{}, fmt.Errorf("minutes must be between 1 and %d", maxSilenceMinutes)
	}
	td.chainsMux.RLock()
	cc := td.Chains[chain]
	td.chainsMux.RUnlock()
	if cc == nil {
		return time.Time{}, errors.New("chain not found")
	}
	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	cc.silencedUntil.Store(until.Unix())
	l(slog.LevelWarn, chain, fmt.Sprintf("🔇 alerts silenced until %s", until.Format(time.RFC3339)))
	return until, nil
}

// unsilenceChain clears any active silence window for the named chain.
func unsilenceChain(chain string) error {
	td.chainsMux.RLock()
	cc := td.Chains[chain]
	td.chainsMux.RUnlock()
	if cc == nil {
		return errors.New("chain not found")
	}
	cc.silencedUntil.Store(0)
	l(slog.LevelInfo, chain, "🔊 alerts unsilenced")
	return nil
}
