package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BlockHeader holds the subset of a TM2 block header used by the height command.
type BlockHeader struct {
	Time         time.Time
	Hash         string // block hash from block_id
	ProposerAddr string // raw address from header.proposer_address
	TxCount      int
	AppHash      string
}

// FetchBlock calls <endpoint>/block?height=<height> and returns the parsed header.
func FetchBlock(endpoint string, height int64) (BlockHeader, error) {
	url := fmt.Sprintf("%s/block?height=%d", strings.TrimRight(endpoint, "/"), height)
	body, err := get(url)
	if err != nil {
		return BlockHeader{}, err
	}
	return parseBlock(body)
}

func parseBlock(data []byte) (BlockHeader, error) {
	var envelope struct {
		Result struct {
			BlockID struct {
				Hash string `json:"hash"`
			} `json:"block_id"`
			Block struct {
				Header map[string]any `json:"header"`
				Data   struct {
					Txs []any `json:"txs"`
				} `json:"data"`
			} `json:"block"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return BlockHeader{}, fmt.Errorf("unmarshal /block: %w", err)
	}
	h := envelope.Result.Block.Header
	bh := BlockHeader{
		Hash:    envelope.Result.BlockID.Hash,
		TxCount: len(envelope.Result.Block.Data.Txs),
	}
	if ts, ok := h["time"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			bh.Time = t.UTC()
		}
	}
	if addr, ok := h["proposer_address"].(string); ok {
		bh.ProposerAddr = addr
	}
	if appHash, ok := h["app_hash"].(string); ok {
		bh.AppHash = appHash
	}
	return bh, nil
}

// CommitSig holds one validator's precommit entry from /commit.
// A nil precommit (absent validator) is represented with Signed=false.
type CommitSig struct {
	ValidatorAddress string
	ValidatorIndex   int
	Round            int
	Signed           bool
}

// FetchCommit calls <endpoint>/commit?height=<height> and returns precommit signatures.
func FetchCommit(endpoint string, height int64) ([]CommitSig, error) {
	url := fmt.Sprintf("%s/commit?height=%d", strings.TrimRight(endpoint, "/"), height)
	body, err := get(url)
	if err != nil {
		return nil, err
	}
	return parseCommit(body)
}

func parseCommit(data []byte) ([]CommitSig, error) {
	// TM2 /commit response wraps the commit in signed_header.
	var envelope struct {
		Result struct {
			SignedHeader struct {
				Commit struct {
					Precommits []map[string]any `json:"precommits"`
				} `json:"commit"`
			} `json:"signed_header"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal /commit: %w", err)
	}
	precommits := envelope.Result.SignedHeader.Commit.Precommits
	sigs := make([]CommitSig, len(precommits))
	for i, pc := range precommits {
		sigs[i].ValidatorIndex = i
		if pc == nil {
			// nil entry = validator did not precommit
			continue
		}
		sigs[i].Signed = true
		if addr, ok := pc["validator_address"].(string); ok {
			sigs[i].ValidatorAddress = addr
		}
		sigs[i].Round = int(anyToInt64(pc["round"]))
	}
	return sigs, nil
}

// ValidatorEntry holds a single entry from /validators.
type ValidatorEntry struct {
	Address     string
	VotingPower int64
}

// FetchValidators calls <endpoint>/validators?height=<height> and returns the validator set.
func FetchValidators(endpoint string, height int64) ([]ValidatorEntry, error) {
	url := fmt.Sprintf("%s/validators?height=%d", strings.TrimRight(endpoint, "/"), height)
	body, err := get(url)
	if err != nil {
		return nil, err
	}
	return parseValidators(body)
}

func parseValidators(data []byte) ([]ValidatorEntry, error) {
	var envelope struct {
		Result struct {
			Validators []map[string]any `json:"validators"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal /validators: %w", err)
	}
	vals := make([]ValidatorEntry, len(envelope.Result.Validators))
	for i, v := range envelope.Result.Validators {
		if addr, ok := v["address"].(string); ok {
			vals[i].Address = addr
		}
		vals[i].VotingPower = int64Field(v, "voting_power")
	}
	return vals, nil
}
