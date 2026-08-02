package tenderduty

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// digester batches non-PagerDuty alert notifications so a burst of simultaneous
// alerts (e.g. many RPC nodes going down together) produces one combined message per
// destination instead of one message per alert. PagerDuty is never batched — it uses
// a DedupKey per alert to resolve individual incidents, which batching would break.
//
// Buffers are keyed by destination identity (webhook URL, bot key + channel), not by
// channel type alone, since different chains can route the same channel type to
// different destinations via per-chain alert config overrides.
type digester struct {
	interval time.Duration

	mu   sync.Mutex
	disc map[string][]*alertMsg // key: discHook
	tg   map[string][]*alertMsg // key: tgKey + "|" + tgChannel
	slk  map[string][]*alertMsg // key: slkHook
	wh   map[string][]*alertMsg // key: whURL
}

func newDigester(interval time.Duration) *digester {
	return &digester{
		interval: interval,
		disc:     make(map[string][]*alertMsg),
		tg:       make(map[string][]*alertMsg),
		slk:      make(map[string][]*alertMsg),
		wh:       make(map[string][]*alertMsg),
	}
}

// enqueue runs shouldNotify exactly once per enabled destination for msg (mirroring
// what the single-alert notify* functions do internally) and buffers msg for the next
// flush if approved. Callers must never also invoke the immediate notify* functions
// for the same msg on the same channel — shouldNotify has dedup/flapping side effects
// and must run exactly once per (msg, destination) pair.
func (d *digester) enqueue(msg *alertMsg) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if msg.disc && shouldNotify(msg, di) {
		d.disc[msg.discHook] = append(d.disc[msg.discHook], msg)
	}
	if msg.tg && shouldNotify(msg, tg) {
		key := msg.tgKey + "|" + msg.tgChannel
		d.tg[key] = append(d.tg[key], msg)
	}
	if msg.slk && shouldNotify(msg, slk) {
		d.slk[msg.slkHook] = append(d.slk[msg.slkHook], msg)
	}
	if msg.wh && shouldNotify(msg, wh) {
		// mirrors notifyWebhook's existing post-shouldNotify DisableResolveMessage filter
		if msg.resolved && boolVal(msg.alertConfig.Webhook.DisableResolveMessage) {
			return
		}
		d.wh[msg.whURL] = append(d.wh[msg.whURL], msg)
	}
}

// run periodically flushes buffered alerts until ctx is cancelled, making one
// best-effort final flush before returning.
func (d *digester) run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.flush()
		case <-ctx.Done():
			d.flush()
			return
		}
	}
}

// flush swaps out all pending buffers and sends one combined message per non-empty
// destination bucket.
func (d *digester) flush() {
	d.mu.Lock()
	disc, tg, slk, wh := d.disc, d.tg, d.slk, d.wh
	d.disc = make(map[string][]*alertMsg)
	d.tg = make(map[string][]*alertMsg)
	d.slk = make(map[string][]*alertMsg)
	d.wh = make(map[string][]*alertMsg)
	d.mu.Unlock()

	for _, msgs := range disc {
		if e := notifyDiscordDigest(msgs); e != nil {
			l(slog.LevelWarn, "error sending discord digest", e.Error())
		}
	}
	for _, msgs := range tg {
		if e := notifyTgDigest(msgs); e != nil {
			l(slog.LevelWarn, "error sending telegram digest", e.Error())
		}
	}
	for _, msgs := range slk {
		if e := notifySlackDigest(msgs); e != nil {
			l(slog.LevelWarn, "error sending slack digest", e.Error())
		}
	}
	for _, msgs := range wh {
		if e := notifyWebhookDigest(msgs); e != nil {
			l(slog.LevelWarn, "error sending webhook digest", e.Error())
		}
	}
}
