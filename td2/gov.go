package tenderduty

import (
	"fmt"
	"time"

	gov "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/gogo/protobuf/proto"
)

// govProposal augments gov.Proposal with a human-readable title/summary that tenderduty
// extracts itself. The pinned cosmos-sdk v0.45 gov.Proposal type predates gov v1's
// title/summary fields, but shares the same protobuf field numbers for id/status/times,
// so unmarshaling a v1 chain's response into it silently drops title/summary as
// unrecognized fields instead of erroring — see extractProposalTitles.
type govProposal struct {
	gov.Proposal
	Title   string
	Summary string
}

// v1ProposalTitleFields and v1QueryProposalsResponseTitles are minimal, hand-written
// mirrors of the cosmos.gov.v1 wire format, carrying only the fields gov.Proposal can't
// represent. Decoded via gogo/protobuf's reflection-based Unmarshal against the same raw
// ABCI response bytes DefaultProvider already fetches — no codegen or dependency bump
// needed, and github.com/gogo/protobuf is already pulled in transitively by cosmos-sdk.
type v1ProposalTitleFields struct {
	ProposalId uint64 `protobuf:"varint,1,opt,name=id,proto3"`
	Title      string `protobuf:"bytes,11,opt,name=title,proto3"`
	Summary    string `protobuf:"bytes,12,opt,name=summary,proto3"`
}

func (*v1ProposalTitleFields) Reset()         {}
func (*v1ProposalTitleFields) String() string { return "" }
func (*v1ProposalTitleFields) ProtoMessage()  {}

type v1QueryProposalsResponseTitles struct {
	Proposals []*v1ProposalTitleFields `protobuf:"bytes,1,rep,name=proposals,proto3"`
}

func (*v1QueryProposalsResponseTitles) Reset()         {}
func (*v1QueryProposalsResponseTitles) String() string { return "" }
func (*v1QueryProposalsResponseTitles) ProtoMessage()  {}

// extractProposalTitles best-effort recovers proposal titles/summaries from a raw
// cosmos.gov.v1.Query/Proposals ABCI response. It never fails the caller: chains that
// are genuinely still on old-style gov (no title/summary in the wire format at all)
// just yield an empty map, and callers should treat missing entries as "title unknown"
// rather than an error.
func extractProposalTitles(raw []byte) map[uint64]struct{ Title, Summary string } {
	result := make(map[uint64]struct{ Title, Summary string })
	if len(raw) == 0 {
		return result
	}
	resp := &v1QueryProposalsResponseTitles{}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return result
	}
	for _, p := range resp.Proposals {
		if p == nil || (p.Title == "" && p.Summary == "") {
			continue
		}
		result[p.ProposalId] = struct{ Title, Summary string }{Title: p.Title, Summary: p.Summary}
	}
	return result
}

// humanDuration renders a duration as a short "2d 3h" / "5h 30m" / "12m" style string,
// rounded to the minute, for use in alert messages.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
