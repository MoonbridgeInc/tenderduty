package tenderduty

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	github_com_cosmos_cosmos_sdk_types "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	distribution "github.com/cosmos/cosmos-sdk/x/distribution/types"
	gov "github.com/cosmos/cosmos-sdk/x/gov/types"
	mint "github.com/cosmos/cosmos-sdk/x/mint/types"
	slashing "github.com/cosmos/cosmos-sdk/x/slashing/types"
	staking "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func ConvertValopertToAccAddress(valoperAddr string) (string, error) {
	// Check if it's a valoper address
	if !strings.Contains(valoperAddr, "valoper") {
		return valoperAddr, nil // Already an account address or something else
	}

	// Decode the address
	prefix, bytes, err := bech32.DecodeAndConvert(valoperAddr)
	if err != nil {
		return "", fmt.Errorf("🛑 failed to decode valoper address: %w", err)
	}

	// Get the base prefix by removing "valoper"
	basePrefix := strings.Replace(prefix, "valoper", "", 1)

	// Re-encode with the base prefix
	accAddress, err := bech32.ConvertAndEncode(basePrefix, bytes)
	if err != nil {
		return "", fmt.Errorf("🛑 failed to encode account address: %w", err)
	}

	return accAddress, nil
}

type DefaultProvider struct {
	ChainConfig *ChainConfig
}

// CheckIfValidatorVoted queries the gov module directly for the validator's vote on a
// proposal, rather than searching tx history: tx_search depends on the queried node's tx
// indexer, which isn't guaranteed to still hold an old vote tx (or to be enabled at all),
// so that approach could flap between "voted" and "not voted" across polls even though the
// vote was cast and recorded on chain long ago. The gov Vote query has no such dependency -
// it reads current chain state - so its answer is stable poll to poll.
func (d *DefaultProvider) CheckIfValidatorVoted(ctx context.Context, proposalID uint64, accAddress string) (bool, error) {
	q := gov.QueryVoteRequest{ProposalId: proposalID, Voter: accAddress}
	b, err := q.Marshal()
	if err != nil {
		return false, err
	}

	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.gov.v1.Query/Vote", b)
	if err != nil {
		return false, fmt.Errorf("🛑 failed to query vote for proposal %d on %s, error: %w", proposalID, d.ChainConfig.name, err)
	}
	if resp == nil || resp.Response.Value == nil {
		// The gov module returns an error response with no value when the voter has not
		// voted on this proposal - that's a normal, expected outcome, not a query failure.
		return false, nil
	}

	// We only care whether a vote record exists, not its contents, so we deliberately
	// don't unmarshal the response into gov.QueryVoteResponse: on gov-v1 chains the vote's
	// weight comes back as a plain decimal string (e.g. "1.000000000000000000"), which the
	// pinned v0.45 gov.WeightedVoteOption's customtype Dec can't parse (it expects the raw
	// scaled-integer text from Dec.Marshal, not a human-readable decimal) - the same
	// v1-vs-v1beta1 wire mismatch extractProposalTitles works around for proposal titles.
	return true, nil
}

// wasPreviouslyUnvoted reports whether proposalID was in the unvoted set as of the last
// successful poll, so a transient vote-check error can preserve that state instead of
// guessing.
func (d *DefaultProvider) wasPreviouslyUnvoted(proposalID uint64) bool {
	for _, p := range d.ChainConfig.unvotedOpenGovProposals {
		if p.ProposalId == proposalID {
			return true
		}
	}
	return false
}

func (d *DefaultProvider) QueryUnvotedOpenProposals(ctx context.Context) ([]govProposal, error) {
	// get all proposals in voting period
	qProposal := gov.QueryProposalsRequest{
		// Filter for only proposals in voting period
		ProposalStatus: gov.StatusVotingPeriod,
	}
	b, err := qProposal.Marshal()
	if err == nil {
		resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.gov.v1.Query/Proposals", b)
		if resp == nil || resp.Response.Value == nil {
			return nil, fmt.Errorf("🛑 failed to query proposals for %s, error: %v", d.ChainConfig.name, err)
		} else {
			proposals := &gov.QueryProposalsResponse{}
			err = proposals.Unmarshal(resp.Response.Value)
			if err == nil {
				// The pinned cosmos-sdk gov.Proposal type has no title/summary fields
				// (see govProposal's doc comment) even though the query above already
				// hits the v1 endpoint that returns them on the wire - recover them
				// separately from the same raw bytes.
				titles := extractProposalTitles(resp.Response.Value)

				// Step 2: Filter out proposals the validator has already voted on
				var unvotedProposals []govProposal

				for _, proposal := range proposals.Proposals {
					// For each proposal, check if the validator has voted
					accAddress, err := ConvertValopertToAccAddress(d.ChainConfig.ValAddress)
					if err != nil {
						l(slog.LevelWarn, fmt.Sprintf("⚠️ Cannot convert valoper to account address: %v", err))
						continue
					}

					hasVoted, err := d.CheckIfValidatorVoted(ctx, proposal.ProposalId, accAddress)
					if err != nil {
						l(slog.LevelWarn, fmt.Sprintf("⚠️ Error checking if validator voted: %v", err))
						// The vote status is unknown for this poll - fall back to what we
						// last knew instead of defaulting to "not voted". Otherwise a
						// transient RPC hiccup fires a false alert, which then immediately
						// "resolves" once the next poll succeeds, spamming notifications
						// for a proposal that was actually voted on long ago.
						hasVoted = !d.wasPreviouslyUnvoted(proposal.ProposalId)
					}

					if !hasVoted {
						title := titles[proposal.ProposalId]
						unvotedProposals = append(unvotedProposals, govProposal{
							Proposal: proposal,
							Title:    title.Title,
							Summary:  title.Summary,
						})
					}
				}

				return unvotedProposals, nil
			}
		}
	}
	return nil, err
}

func (d *DefaultProvider) QueryDenomMetadata(ctx context.Context, denom string) (medatada *bank.Metadata, err error) {
	queryParams := bank.QueryDenomMetadataRequest{
		Denom: denom,
	}
	b, err := queryParams.Marshal()
	if err != nil {
		return nil, err
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.bank.v1beta1.Query/DenomMetadata", b)
	if err != nil {
		return nil, err
	}
	if resp.Response.Value == nil {
		return nil, errors.New("could not find denom metadata")
	}
	val := &bank.QueryDenomMetadataResponse{}
	err = val.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, err
	}
	return &val.Metadata, nil
}

func (d *DefaultProvider) QueryValidatorSelfDelegationRewardsAndCommission(ctx context.Context) (rewards *github_com_cosmos_cosmos_sdk_types.DecCoins, commission *github_com_cosmos_cosmos_sdk_types.DecCoins, err error) {
	accAddress, err := ConvertValopertToAccAddress(d.ChainConfig.ValAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("🛑 failed to decode valoper address: %w", err)
	}

	rewardsQueryParams := distribution.QueryDelegationRewardsRequest{
		DelegatorAddress: accAddress,
		ValidatorAddress: d.ChainConfig.ValAddress,
	}
	b, err := rewardsQueryParams.Marshal()
	if err != nil {
		return nil, nil, err
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.distribution.v1beta1.Query/DelegationRewards", b)
	if err != nil {
		return nil, nil, err
	}
	if resp.Response.Value == nil {
		return nil, nil, errors.New("could not query self-delegation rewards for validator " + d.ChainConfig.ValAddress)
	}
	rewardsResponse := &distribution.QueryDelegationRewardsResponse{}
	err = rewardsResponse.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, nil, err
	}

	commissionQueryParams := distribution.QueryValidatorCommissionRequest{
		ValidatorAddress: d.ChainConfig.ValAddress,
	}
	b, err = commissionQueryParams.Marshal()
	if err != nil {
		return nil, nil, err
	}
	resp, err = d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.distribution.v1beta1.Query/ValidatorCommission", b)
	if err != nil {
		return nil, nil, err
	}
	if resp.Response.Value == nil {
		return nil, nil, errors.New("could not query commission for validator " + d.ChainConfig.ValAddress)
	}
	commissionResponse := &distribution.QueryValidatorCommissionResponse{}
	err = commissionResponse.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, nil, err
	}
	return &rewardsResponse.Rewards, &commissionResponse.Commission.Commission, nil
}

func (d *DefaultProvider) QueryValidatorVotingPool(ctx context.Context) (votingPool *staking.Pool, err error) {
	queryParams := staking.QueryPoolRequest{}
	b, err := queryParams.Marshal()
	if err != nil {
		return nil, err
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.staking.v1beta1.Query/Pool", b)
	if err != nil {
		return nil, err
	}
	if resp.Response.Value == nil {
		return nil, errors.New("could not query the staking pool information for validator " + d.ChainConfig.ValAddress)
	}
	val := &staking.QueryPoolResponse{}
	err = val.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, err
	}
	return &val.Pool, nil
}

func (d *DefaultProvider) QueryValidatorInfo(ctx context.Context) (pub []byte, moniker string, jailed bool, bonded bool, delegatedTokens float64, commissionRate float64, err error) {
	if strings.Contains(d.ChainConfig.ValAddress, "valcons") {
		_, bz, err := bech32.DecodeAndConvert(d.ChainConfig.ValAddress)
		if err != nil {
			return nil, "", false, false, 0, 0, errors.New("could not decode and convert your address" + d.ChainConfig.ValAddress)
		}

		hexAddress := fmt.Sprintf("%X", bz)
		return ToBytes(hexAddress), d.ChainConfig.ValAddress, false, true, 0, 0, nil
	}

	q := staking.QueryValidatorRequest{
		ValidatorAddr: d.ChainConfig.ValAddress,
	}
	b, err := q.Marshal()
	if err != nil {
		return
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.staking.v1beta1.Query/Validator", b)
	if err != nil {
		return
	}
	if resp.Response.Value == nil {
		return nil, "", false, false, 0, 0, errors.New("could not find validator " + d.ChainConfig.ValAddress)
	}
	val := &staking.QueryValidatorResponse{}
	err = val.Unmarshal(resp.Response.Value)
	if err != nil {
		return
	}
	if val.Validator.ConsensusPubkey == nil {
		return nil, "", false, false, 0, 0, errors.New("got invalid consensus pubkey for " + d.ChainConfig.ValAddress)
	}

	pubBytes := make([]byte, 0)
	switch val.Validator.ConsensusPubkey.TypeUrl {
	case "/cosmos.crypto.ed25519.PubKey":
		pk := ed25519.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	case "/cosmos.crypto.secp256k1.PubKey":
		pk := secp256k1.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	}
	if len(pubBytes) == 0 {
		return nil, "", false, false, 0, 0, errors.New("could not get pubkey for" + d.ChainConfig.ValAddress)
	}

	return pubBytes, val.Validator.GetMoniker(), val.Validator.Jailed, val.Validator.Status == 3, val.Validator.Tokens.ToDec().MustFloat64(), val.Validator.Commission.Rate.MustFloat64(), nil
}

func (d *DefaultProvider) QuerySigningInfo(ctx context.Context) (*slashing.ValidatorSigningInfo, error) {
	// get current signing information (tombstoned, missed block count)
	qSigning := slashing.QuerySigningInfoRequest{ConsAddress: d.ChainConfig.valInfo.Valcons}
	b, err := qSigning.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal signing info request: %w", err)
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.slashing.v1beta1.Query/SigningInfo", b)
	if resp == nil || resp.Response.Value == nil {
		return nil, fmt.Errorf("query signing info: %w", err)
	}
	info := &slashing.QuerySigningInfoResponse{}
	err = info.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, fmt.Errorf("unmarshal signing info response: %w", err)
	}

	return &info.ValSigningInfo, nil
}

func (d *DefaultProvider) QuerySlashingParams(ctx context.Context) (*slashing.Params, error) {
	qParams := &slashing.QueryParamsRequest{}
	b, err := qParams.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal slashing params: %w", err)
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.slashing.v1beta1.Query/Params", b)
	if err != nil {
		return nil, fmt.Errorf("query slashing params: %w", err)
	}
	if resp.Response.Value == nil {
		return nil, errors.New("🛑 could not query slashing params, got empty response")
	}
	params := &slashing.QueryParamsResponse{}
	err = params.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, fmt.Errorf("unmarshal slashing params: %w", err)
	}
	return &params.Params, nil
}

func (d *DefaultProvider) QueryStakingParams(ctx context.Context) (*staking.Params, error) {
	qParams := &staking.QueryParamsRequest{}
	b, err := qParams.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal staking params: %w", err)
	}
	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.staking.v1beta1.Query/Params", b)
	if err != nil {
		return nil, fmt.Errorf("query staking params: %w", err)
	}
	if resp.Response.Value == nil {
		return nil, errors.New("🛑 could not query staking params, got empty response")
	}
	params := &staking.QueryParamsResponse{}
	err = params.Unmarshal(resp.Response.Value)
	if err != nil {
		return nil, fmt.Errorf("unmarshal staking params: %w", err)
	}
	return &params.Params, nil
}

func (d *DefaultProvider) QueryChainInfo(ctx context.Context) (totalSupply float64, communityTax float64, inflationRate float64, err error) {
	// Query total supply using bank module
	supplyQueryParams := bank.QuerySupplyOfRequest{
		Denom: d.ChainConfig.denomMetadata.Base,
	}
	b, err := supplyQueryParams.Marshal()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("marshal total supply request: %w", err)
	}

	resp, err := d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.bank.v1beta1.Query/SupplyOf", b)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query total supply: %w", err)
	}

	if resp.Response.Value == nil {
		return 0, 0, 0, errors.New("could not query total supply")
	}

	supplyResponse := &bank.QuerySupplyOfResponse{}
	err = supplyResponse.Unmarshal(resp.Response.Value)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("unmarshal total supply response: %w", err)
	}

	totalSupply = supplyResponse.Amount.Amount.ToDec().MustFloat64()

	// Query community tax using distribution module
	distQueryParams := distribution.QueryParamsRequest{}
	b, err = distQueryParams.Marshal()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("marshal distribution params request: %w", err)
	}

	resp, err = d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.distribution.v1beta1.Query/Params", b)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query distribution params: %w", err)
	}

	if resp.Response.Value == nil {
		return 0, 0, 0, errors.New("could not query distribution params")
	}

	distResponse := &distribution.QueryParamsResponse{}
	err = distResponse.Unmarshal(resp.Response.Value)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("unmarshal distribution params response: %w", err)
	}

	communityTax = distResponse.Params.CommunityTax.MustFloat64()

	// Query current inflation rate using mint module
	inflationQuery := mint.QueryInflationRequest{}
	b, err = inflationQuery.Marshal()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("marshal inflation request: %w", err)
	}

	resp, err = d.ChainConfig.client.ABCIQuery(ctx, "/cosmos.mint.v1beta1.Query/Inflation", b)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query inflation: %w", err)
	}

	inflationRate = 0.0
	if resp.Response.Value != nil {
		inflationResponse := &mint.QueryInflationResponse{}
		err = inflationResponse.Unmarshal(resp.Response.Value)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("unmarshal inflation response: %w", err)
		}

		inflationRate = inflationResponse.Inflation.MustFloat64()
	}

	return totalSupply, communityTax, inflationRate, nil
}
