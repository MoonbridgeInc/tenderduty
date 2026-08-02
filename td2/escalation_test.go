package tenderduty

import (
	"sync"
	"testing"
	"time"
)

func newEscalationTestConfig() *Config {
	trueBool := true
	return &Config{
		chainsMux: sync.RWMutex{},
		Chains: map[string]*ChainConfig{
			"test-chain": {
				name:       "test-chain",
				ChainId:    "test-chain-1",
				ValAddress: "testval123",
				Alerts: AlertConfig{
					Pagerduty: PDConfig{Enabled: &trueBool},
					Discord:   DiscordConfig{Enabled: &trueBool},
					Telegram:  TeleConfig{Enabled: &trueBool},
					Slack:     SlackConfig{Enabled: &trueBool},
					Webhook:   WebhookConfig{Enabled: &trueBool},
				},
			},
		},
		DefaultAlertConfig: AlertConfig{
			Pagerduty: PDConfig{Enabled: &trueBool},
			Discord:   DiscordConfig{Enabled: &trueBool},
			Telegram:  TeleConfig{Enabled: &trueBool},
			Slack:     SlackConfig{Enabled: &trueBool},
			Webhook:   WebhookConfig{Enabled: &trueBool},
		},
		alertChan: make(chan *alertMsg, 10),
	}
}

func newEscalationTestAlarms() *alarmCache {
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

func TestCheckEscalationsFiresPastThreshold(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	alarms.AllAlarms["test-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:  "node is down",
			SentTime: time.Now().Add(-5 * time.Minute),
			Severity: "critical",
		},
	}

	checkEscalations(1 * time.Minute)

	select {
	case msg := <-td.alertChan:
		if msg.uniqueId != "SomeAlert_testval123_escalated" {
			t.Errorf("expected derived uniqueId, got %q", msg.uniqueId)
		}
		if msg.pd {
			t.Error("expected escalation message to never target PagerDuty (msg.pd must be false)")
		}
		if msg.resolved {
			t.Error("expected escalation message to be a firing (not resolved) alert")
		}
		if msg.severity != "critical" {
			t.Errorf("expected severity critical, got %q", msg.severity)
		}
	default:
		t.Fatal("expected an escalation message on td.alertChan")
	}

	entry := alarms.AllAlarms["test-chain"]["SomeAlert_testval123"]
	if !entry.Escalated {
		t.Error("expected the original alarm entry to be marked Escalated")
	}
}

func TestCheckEscalationsTooYoung(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	alarms.AllAlarms["test-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:  "node is down",
			SentTime: time.Now().Add(-10 * time.Second),
			Severity: "critical",
		},
	}

	checkEscalations(5 * time.Minute)

	select {
	case msg := <-td.alertChan:
		t.Fatalf("expected no escalation for an alert younger than the threshold, got %+v", msg)
	default:
	}
	if alarms.AllAlarms["test-chain"]["SomeAlert_testval123"].Escalated {
		t.Error("expected Escalated to remain false")
	}
}

func TestCheckEscalationsNonCritical(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	alarms.AllAlarms["test-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:  "empty blocks",
			SentTime: time.Now().Add(-10 * time.Minute),
			Severity: "warning",
		},
	}

	checkEscalations(1 * time.Minute)

	select {
	case msg := <-td.alertChan:
		t.Fatalf("expected non-critical alerts to never escalate, got %+v", msg)
	default:
	}
}

func TestCheckEscalationsAlreadyEscalated(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	alarms.AllAlarms["test-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:   "node is down",
			SentTime:  time.Now().Add(-10 * time.Minute),
			Severity:  "critical",
			Escalated: true,
		},
	}

	checkEscalations(1 * time.Minute)

	select {
	case msg := <-td.alertChan:
		t.Fatalf("expected no duplicate escalation, got %+v", msg)
	default:
	}
}

func TestCheckEscalationsSilencedChain(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	td.Chains["test-chain"].silencedUntil.Store(time.Now().Add(10 * time.Minute).Unix())

	alarms.AllAlarms["test-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:  "node is down",
			SentTime: time.Now().Add(-10 * time.Minute),
			Severity: "critical",
		},
	}

	checkEscalations(1 * time.Minute)

	select {
	case msg := <-td.alertChan:
		t.Fatalf("expected no escalation while the chain is silenced, got %+v", msg)
	default:
	}
	// Must stay false so a later tick retries for real once the silence window ends —
	// this is the regression case caught during design review.
	if alarms.AllAlarms["test-chain"]["SomeAlert_testval123"].Escalated {
		t.Error("expected Escalated to remain false while silenced, so escalation retries after silence expires")
	}
}

func TestCheckEscalationsUnknownChain(t *testing.T) {
	originalTd, originalAlarms := td, alarms
	td = newEscalationTestConfig()
	alarms = newEscalationTestAlarms()
	defer func() { td, alarms = originalTd, originalAlarms }()

	alarms.AllAlarms["removed-chain"] = map[string]alertMsgCache{
		"SomeAlert_testval123": {
			Message:  "node is down",
			SentTime: time.Now().Add(-10 * time.Minute),
			Severity: "critical",
		},
	}

	checkEscalations(1 * time.Minute) // must not panic

	select {
	case msg := <-td.alertChan:
		t.Fatalf("expected no escalation for a chain no longer in td.Chains, got %+v", msg)
	default:
	}
}
