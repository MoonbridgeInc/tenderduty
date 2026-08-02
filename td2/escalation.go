package tenderduty

import (
	"context"
	"fmt"
	"time"
)

// escalationSweep periodically checks for still-unresolved critical alerts that have
// outlived threshold and re-sends each one once. The sweep cadence is independent of
// threshold (minutes-granularity) — correctness lives entirely in the
// time.Since(SentTime) >= threshold check in checkEscalations; the cadence only
// bounds how late (worst case ~30s) an escalation fires relative to the threshold.
func escalationSweep(ctx context.Context, threshold time.Duration) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			checkEscalations(threshold)
		case <-ctx.Done():
			return
		}
	}
}

type escalationCandidate struct {
	chain, alertID, message string
}

// checkEscalations re-sends any severity="critical" alarm that has been active for at
// least threshold and hasn't already been escalated. It never touches PagerDuty
// (which has its own native escalation policies) and never escalates past a currently
// silenced chain — in that case the candidate is simply skipped, not marked
// Escalated, so a later tick retries it for real once the silence window ends.
func checkEscalations(threshold time.Duration) {
	var due []escalationCandidate

	// Phase 1: scan for candidates without mutating anything yet.
	alarms.notifyMux.Lock()
	for chain, byID := range alarms.AllAlarms {
		for alertID, cache := range byID {
			if cache.Severity == "critical" && !cache.Escalated && time.Since(cache.SentTime) >= threshold {
				due = append(due, escalationCandidate{chain, alertID, cache.Message})
			}
		}
	}
	alarms.notifyMux.Unlock()

	for _, cand := range due {
		td.chainsMux.RLock()
		cc := td.Chains[cand.chain]
		td.chainsMux.RUnlock()
		if cc == nil {
			continue
		}
		if silenced, _ := cc.silenceStatus(); silenced {
			continue // respect an active maintenance window; retried on a later tick
		}

		// A derived unique ID is required: shouldNotify's per-channel dedup would
		// otherwise silently suppress a re-send using the original alertID, since
		// that ID is already recorded as "sent, not resolved" on every channel.
		msg, _, ok := buildAlertMsg(td, cand.chain,
			fmt.Sprintf("🔺 ESCALATION: still unresolved after %d+ min — %s", int(threshold.Minutes()), cand.message),
			"critical", false, cand.alertID+"_escalated")
		if !ok {
			continue
		}
		msg.pd = false // PagerDuty has its own native escalation policies; never touch it here

		// Phase 3: re-check under lock (the alert may have resolved or already been
		// escalated by a concurrent sweep between phase 1 and here) before marking.
		alarms.notifyMux.Lock()
		entry, exists := alarms.AllAlarms[cand.chain][cand.alertID]
		if !exists || entry.Escalated {
			alarms.notifyMux.Unlock()
			continue
		}
		entry.Escalated = true
		alarms.AllAlarms[cand.chain][cand.alertID] = entry
		alarms.notifyMux.Unlock()

		recordAlertHistory(td, cc, msg.message, msg.severity, msg.resolved)
		td.alertChan <- msg
	}
}
