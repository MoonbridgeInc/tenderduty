package tenderduty

import (
	"encoding/json"
	"testing"
)

func newEvidenceReply(evidenceType string, value string) *WsReply {
	reply := &WsReply{}
	reply.Result.Data.Type = "tendermint/event/NewEvidence"
	payload := `{"evidence":{"type":"` + evidenceType + `","value":` + value + `},"height":"100"}`
	reply.Result.Data.Value = json.RawMessage(payload)
	return reply
}

func TestParseDuplicateVoteEvidence(t *testing.T) {
	tests := []struct {
		name          string
		reply         *WsReply
		expectOk      bool
		expectHeightA int64
		expectRoundA  int32
		expectAddrA   string
		expectAddrB   string
	}{
		{
			name: "valid duplicate vote evidence",
			reply: newEvidenceReply("tendermint/DuplicateVoteEvidence",
				`{"vote_a":{"height":"12345","round":2,"validator_address":"AAAABBBBCCCCDDDD"},`+
					`"vote_b":{"height":"12345","round":2,"validator_address":"AAAABBBBCCCCDDDD"}}`),
			expectOk:      true,
			expectHeightA: 12345,
			expectRoundA:  2,
			expectAddrA:   "AAAABBBBCCCCDDDD",
			expectAddrB:   "AAAABBBBCCCCDDDD",
		},
		{
			name: "light client attack evidence is out of scope",
			reply: newEvidenceReply("tendermint/LightClientAttackEvidence",
				`{"ConflictingBlock":{},"CommonHeight":100}`),
			expectOk: false,
		},
		{
			name: "malformed evidence value",
			reply: newEvidenceReply("tendermint/DuplicateVoteEvidence",
				`{"vote_a": this is not valid json`),
			expectOk: false,
		},
		{
			name: "malformed outer payload",
			reply: func() *WsReply {
				reply := &WsReply{}
				reply.Result.Data.Value = json.RawMessage(`not json at all`)
				return reply
			}(),
			expectOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dve, ok := parseDuplicateVoteEvidence(tt.reply)
			if ok != tt.expectOk {
				t.Fatalf("expected ok=%v, got %v", tt.expectOk, ok)
			}
			if !ok {
				return
			}
			if dve.VoteA.Height.val() != tt.expectHeightA {
				t.Errorf("expected VoteA height %d, got %d", tt.expectHeightA, dve.VoteA.Height.val())
			}
			if dve.VoteA.Round != tt.expectRoundA {
				t.Errorf("expected VoteA round %d, got %d", tt.expectRoundA, dve.VoteA.Round)
			}
			if dve.VoteA.ValidatorAddress != tt.expectAddrA {
				t.Errorf("expected VoteA address %s, got %s", tt.expectAddrA, dve.VoteA.ValidatorAddress)
			}
			if dve.VoteB.ValidatorAddress != tt.expectAddrB {
				t.Errorf("expected VoteB address %s, got %s", tt.expectAddrB, dve.VoteB.ValidatorAddress)
			}
		})
	}
}

func TestParseDuplicateVoteEvidenceAddressMatching(t *testing.T) {
	const ourAddress = "AAAABBBBCCCCDDDD"
	const otherAddress = "1111222233334444"

	tests := []struct {
		name        string
		addrA       string
		addrB       string
		expectMatch bool
	}{
		{"matches via VoteA", ourAddress, otherAddress, true},
		{"matches via VoteB", otherAddress, ourAddress, true},
		{"matches neither", otherAddress, otherAddress, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := newEvidenceReply("tendermint/DuplicateVoteEvidence",
				`{"vote_a":{"height":"100","round":0,"validator_address":"`+tt.addrA+`"},`+
					`"vote_b":{"height":"100","round":0,"validator_address":"`+tt.addrB+`"}}`)
			dve, ok := parseDuplicateVoteEvidence(reply)
			if !ok {
				t.Fatalf("expected evidence to parse successfully")
			}
			match := dve.VoteA.ValidatorAddress == ourAddress || dve.VoteB.ValidatorAddress == ourAddress
			if match != tt.expectMatch {
				t.Errorf("expected match=%v, got %v", tt.expectMatch, match)
			}
		})
	}
}
