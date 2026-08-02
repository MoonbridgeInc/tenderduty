package tenderduty

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
)

func TestDigestSummary(t *testing.T) {
	tests := []struct {
		name     string
		msgs     []*alertMsg
		expected string
	}{
		{
			name: "single chain single firing alert",
			msgs: []*alertMsg{
				{chain: "osmosis", message: "m1", resolved: false},
			},
			expected: "1 new alert(s), 0 resolved\n\n🚨 osmosis:\n- m1",
		},
		{
			name: "single chain multiple firing alerts",
			msgs: []*alertMsg{
				{chain: "osmosis", message: "m1", resolved: false},
				{chain: "osmosis", message: "m2", resolved: false},
			},
			expected: "2 new alert(s), 0 resolved\n\n🚨 osmosis:\n- m1\n- m2",
		},
		{
			name: "multiple chains mixed firing and resolved",
			msgs: []*alertMsg{
				{chain: "osmosis", message: "m1", resolved: false},
				{chain: "cosmoshub", message: "m2", resolved: false},
				{chain: "osmosis", message: "m3", resolved: true},
			},
			expected: "2 new alert(s), 1 resolved\n\n🚨 osmosis:\n- m1\n\n💜 osmosis (resolved):\n- m3\n\n🚨 cosmoshub:\n- m2",
		},
		{
			name: "all resolved",
			msgs: []*alertMsg{
				{chain: "osmosis", message: "m1", resolved: true},
			},
			expected: "0 new alert(s), 1 resolved\n\n💜 osmosis (resolved):\n- m1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := digestSummary(tt.msgs)
			if got != tt.expected {
				t.Errorf("digestSummary() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestBuildDiscordDigestMessage(t *testing.T) {
	msgs := []*alertMsg{
		{chain: "osmosis", message: "m1", resolved: false},
		{chain: "osmosis", message: "m2", resolved: true},
	}
	got := buildDiscordDigestMessage(msgs)
	expected := &DiscordMessage{
		Username: "Tenderduty",
		Content:  "📋 Alert digest: 1 new, 1 resolved",
		Embeds: []DiscordEmbed{{
			Description: digestSummary(msgs),
		}},
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("buildDiscordDigestMessage() = %+v, want %+v", got, expected)
	}
}

func TestBuildSlackDigestMessage(t *testing.T) {
	msgs := []*alertMsg{
		{chain: "osmosis", message: "m1", resolved: false, slkMentions: "@here"},
	}
	got := buildSlackDigestMessage(msgs)
	if got.Text != digestSummary(msgs) {
		t.Errorf("expected Text %q, got %q", digestSummary(msgs), got.Text)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Color != "danger" {
		t.Errorf("expected one danger-colored attachment, got %+v", got.Attachments)
	}

	resolvedMsgs := []*alertMsg{
		{chain: "osmosis", message: "m1", resolved: true},
	}
	gotResolved := buildSlackDigestMessage(resolvedMsgs)
	if gotResolved.Attachments[0].Color != "good" {
		t.Errorf("expected good-colored attachment for all-resolved digest, got %q", gotResolved.Attachments[0].Color)
	}
}

func TestBuildWebhookDigestPayload(t *testing.T) {
	firingMsg := &alertMsg{chain: "osmosis", message: "m1", uniqueId: "id1", resolved: false}
	resolvedMsg := &alertMsg{chain: "osmosis", message: "m2", uniqueId: "id2", resolved: true}

	payload := buildWebhookDigestPayload([]*alertMsg{firingMsg, resolvedMsg})
	if len(payload.Alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(payload.Alerts))
	}
	if payload.Status != "firing" {
		t.Errorf("expected overall status 'firing' when at least one alert is firing, got %q", payload.Status)
	}
	if payload.GroupKey != "tenderduty-digest" {
		t.Errorf("expected GroupKey 'tenderduty-digest', got %q", payload.GroupKey)
	}
	if !reflect.DeepEqual(payload.Alerts[0], buildWebhookAlert(firingMsg)) {
		t.Errorf("expected first alert to match buildWebhookAlert(firingMsg)")
	}
	if !reflect.DeepEqual(payload.Alerts[1], buildWebhookAlert(resolvedMsg)) {
		t.Errorf("expected second alert to match buildWebhookAlert(resolvedMsg)")
	}

	allResolved := buildWebhookDigestPayload([]*alertMsg{resolvedMsg})
	if allResolved.Status != "resolved" {
		t.Errorf("expected overall status 'resolved' when every alert is resolved, got %q", allResolved.Status)
	}
}

func newTestAlarmsForDigest() *alarmCache {
	return &alarmCache{
		SentPdAlarms:   make(map[string]alertMsgCache),
		SentTgAlarms:   make(map[string]alertMsgCache),
		SentDiAlarms:   make(map[string]alertMsgCache),
		SentSlkAlarms:  make(map[string]alertMsgCache),
		SentWHAlarms:   make(map[string]alertMsgCache),
		AllAlarms:      make(map[string]map[string]alertMsgCache),
		flappingAlarms: make(map[string]map[string]alertMsgCache),
		notifyMux:      sync.RWMutex{},
	}
}

func TestDigesterEnqueue(t *testing.T) {
	originalAlarms := alarms
	alarms = newTestAlarmsForDigest()
	defer func() { alarms = originalAlarms }()

	infoThreshold := AlertConfig{
		Discord:  DiscordConfig{SeverityThreshold: "info"},
		Telegram: TeleConfig{SeverityThreshold: "info"},
		Slack:    SlackConfig{SeverityThreshold: "info"},
		Webhook:  WebhookConfig{SeverityThreshold: "info"},
	}

	d := newDigester(0) // interval is irrelevant to enqueue itself

	// A discord-only message must not leak into any other channel's buffer.
	discOnly := &alertMsg{
		disc: true, tg: false, slk: false, wh: false,
		chain: "osmosis", message: "node down", severity: "critical",
		uniqueId: "enqueue_test_1", discHook: "https://discord.example/a",
		alertConfig: &infoThreshold,
	}
	d.enqueue(discOnly)
	if len(d.disc) != 1 || len(d.disc["https://discord.example/a"]) != 1 {
		t.Fatalf("expected discOnly to land in d.disc, got disc=%v tg=%v slk=%v wh=%v", d.disc, d.tg, d.slk, d.wh)
	}
	if len(d.tg) != 0 || len(d.slk) != 0 || len(d.wh) != 0 {
		t.Fatalf("expected discOnly to NOT leak into other channel buffers")
	}

	// Two chains with different discord webhooks must land in separate buckets.
	discOther := &alertMsg{
		disc: true, chain: "cosmoshub", message: "node down", severity: "critical",
		uniqueId: "enqueue_test_2", discHook: "https://discord.example/b",
		alertConfig: &infoThreshold,
	}
	d.enqueue(discOther)
	if len(d.disc) != 2 {
		t.Fatalf("expected two separate destination buckets, got %d: %v", len(d.disc), d.disc)
	}

	// A resolved webhook message with DisableResolveMessage=true must be dropped.
	disableResolve := true
	skipped := &alertMsg{
		wh: true, chain: "osmosis", message: "resolved", severity: "critical",
		resolved: true, uniqueId: "enqueue_test_3", whURL: "https://wh.example/a",
		alertConfig: &AlertConfig{Webhook: WebhookConfig{SeverityThreshold: "info", DisableResolveMessage: &disableResolve}},
	}
	d.enqueue(skipped)
	if len(d.wh) != 0 {
		t.Fatalf("expected resolved+DisableResolveMessage alert to be dropped from the webhook buffer, got %v", d.wh)
	}

	// Duplicate enqueue of the same non-resolved alert ID should only buffer once
	// (dedup still applies through shouldNotify on the digest path).
	dup := &alertMsg{
		slk: true, chain: "osmosis", message: "dup", severity: "critical",
		uniqueId: "enqueue_test_4", slkHook: "https://slack.example/a",
		alertConfig: &infoThreshold,
	}
	d.enqueue(dup)
	d.enqueue(dup)
	if len(d.slk["https://slack.example/a"]) != 1 {
		t.Fatalf("expected duplicate enqueue to be deduped, got %d buffered", len(d.slk["https://slack.example/a"]))
	}
}

func TestDigesterFlushClearsBuffers(t *testing.T) {
	originalAlarms := alarms
	alarms = newTestAlarmsForDigest()
	defer func() { alarms = originalAlarms }()

	infoThreshold := AlertConfig{Discord: DiscordConfig{SeverityThreshold: "info"}}
	d := newDigester(0)
	d.enqueue(&alertMsg{
		disc: true, chain: "osmosis", message: "m", severity: "critical",
		uniqueId: "flush_test_1", discHook: "https://discord.example/a",
		alertConfig: &infoThreshold,
	})
	if len(d.disc) == 0 {
		t.Fatal("expected something buffered before flush")
	}

	// flush() sends real HTTP requests for non-empty buckets; mock the transport
	// (same pattern as TestNotifySlack/TestNotifyWebhook) so this stays a fast,
	// network-free unit test. http.Client{} with a nil Transport falls back to
	// http.DefaultTransport, which is what notifyDiscordDigest uses.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 204, // Discord's success code
			Body:       io.NopCloser(bytes.NewBuffer(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	d.flush()

	if len(d.disc) != 0 || len(d.tg) != 0 || len(d.slk) != 0 || len(d.wh) != 0 {
		t.Errorf("expected all buffers empty after flush, got disc=%v tg=%v slk=%v wh=%v", d.disc, d.tg, d.slk, d.wh)
	}
}

// TestDigesterCombinesStormIntoOneRequest is the core value proposition of digest
// mode: N alerts hitting the same destination in a burst (e.g. many RPC nodes going
// down together) must produce exactly ONE outbound HTTP request, not N.
func TestDigesterCombinesStormIntoOneRequest(t *testing.T) {
	originalAlarms := alarms
	alarms = newTestAlarmsForDigest()
	defer func() { alarms = originalAlarms }()

	callCount := 0
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		return &http.Response{
			StatusCode: 204,
			Body:       io.NopCloser(bytes.NewBuffer(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	infoThreshold := &AlertConfig{Discord: DiscordConfig{SeverityThreshold: "info"}}
	d := newDigester(0)
	for i := 0; i < 5; i++ {
		d.enqueue(&alertMsg{
			disc: true, chain: "osmosis", message: fmt.Sprintf("RPC node %d down", i),
			severity: "critical", uniqueId: fmt.Sprintf("storm_test_%d", i),
			discHook: "https://discord.example/same-destination", alertConfig: infoThreshold,
		})
	}
	if len(d.disc["https://discord.example/same-destination"]) != 5 {
		t.Fatalf("expected 5 alerts buffered before flush, got %d", len(d.disc["https://discord.example/same-destination"]))
	}

	d.flush()

	if callCount != 1 {
		t.Errorf("expected exactly 1 HTTP request for 5 alerts to the same destination, got %d", callCount)
	}
}
