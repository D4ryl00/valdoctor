// Package rpc provides a lightweight client for TM2 JSON-RPC endpoints.
// It is used as an optional enrichment step when RPC endpoints are configured
// in the metadata file.
package rpc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// ABCIInfo holds the response of the /abci_info endpoint.
type ABCIInfo struct {
	AppVersion string `json:"app_version"`
	LastBlockHeight int64  `json:"last_block_height"`
	LastBlockAppHash string `json:"last_block_app_hash"`
}

// TxResult is a single transaction result from /block_results.
type TxResult struct {
	GasWanted int64  `json:"gas_wanted"`
	GasUsed   int64  `json:"gas_used"`
	Error     string `json:"error,omitempty"`
}

// BlockResults holds the relevant fields from the /block_results response.
type BlockResults struct {
	Height     int64      `json:"height"`
	DeliverTxs []TxResult `json:"deliver_txs,omitempty"`
}

// FetchABCIInfo calls <endpoint>/abci_info and returns the parsed response.
func FetchABCIInfo(endpoint string) (ABCIInfo, error) {
	url := strings.TrimRight(endpoint, "/") + "/abci_info"
	body, err := get(url)
	if err != nil {
		return ABCIInfo{}, err
	}
	return parseABCIInfo(body)
}

// FetchBlockResults calls <endpoint>/block_results?height=<height> and returns
// the parsed response.
func FetchBlockResults(endpoint string, height int64) (BlockResults, error) {
	url := fmt.Sprintf("%s/block_results?height=%d", strings.TrimRight(endpoint, "/"), height)
	body, err := get(url)
	if err != nil {
		return BlockResults{}, err
	}
	return parseBlockResults(body, height)
}

// ConsensusState holds the parsed fields from the /consensus_state response.
type ConsensusState struct {
	Height int64  `json:"height"`
	Round  int    `json:"round"`
	Step   string `json:"step"` // e.g. "Prevote", "Precommit"

	ProposalBlockHash string `json:"proposal_block_hash,omitempty"`
	LockedBlockHash   string `json:"locked_block_hash,omitempty"`

	PrevotesReceived   int  `json:"prevotes_received,omitempty"`
	PrevotesTotal      int  `json:"prevotes_total,omitempty"`
	PrevotesMaj23      bool `json:"prevotes_maj23,omitempty"`
	PrecommitsReceived int  `json:"precommits_received,omitempty"`
	PrecommitsTotal    int  `json:"precommits_total,omitempty"`
	PrecommitsMaj23    bool `json:"precommits_maj23,omitempty"`
}

// FetchConsensusState calls <endpoint>/consensus_state and returns the parsed response.
func FetchConsensusState(endpoint string) (ConsensusState, error) {
	url := strings.TrimRight(endpoint, "/") + "/consensus_state"
	body, err := get(url)
	if err != nil {
		return ConsensusState{}, err
	}
	return parseConsensusState(body)
}

func parseConsensusState(data []byte) (ConsensusState, error) {
	var envelope struct {
		Result struct {
			RoundState struct {
				HRS               string `json:"height/round/step"`
				ProposalBlockHash string `json:"proposal_block_hash"`
				LockedBlockHash   string `json:"locked_block_hash"`
				HeightVoteSet     []struct {
					Round              int    `json:"round"`
					PrevotesBitArray   string `json:"prevotes_bit_array"`
					PrecommitsBitArray string `json:"precommits_bit_array"`
				} `json:"height_vote_set"`
			} `json:"round_state"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ConsensusState{}, fmt.Errorf("unmarshal consensus_state: %w", err)
	}
	rs := envelope.Result.RoundState
	cs := ConsensusState{
		ProposalBlockHash: rs.ProposalBlockHash,
		LockedBlockHash:   rs.LockedBlockHash,
	}
	var stepNum int
	fmt.Sscanf(rs.HRS, "%d/%d/%d", &cs.Height, &cs.Round, &stepNum)
	cs.Step = roundStepName(stepNum)

	// Extract vote counts for the current round from the bit arrays.
	for _, rvs := range rs.HeightVoteSet {
		if rvs.Round != cs.Round {
			continue
		}
		cs.PrevotesReceived, cs.PrevotesTotal, cs.PrevotesMaj23 = parseBitArray(rvs.PrevotesBitArray)
		cs.PrecommitsReceived, cs.PrecommitsTotal, cs.PrecommitsMaj23 = parseBitArray(rvs.PrecommitsBitArray)
		break
	}
	return cs, nil
}

// roundStepName maps a TM2 RoundStepType integer to its short name.
func roundStepName(step int) string {
	switch step {
	case 1:
		return "NewHeight"
	case 2:
		return "NewRound"
	case 3:
		return "Propose"
	case 4:
		return "Prevote"
	case 5:
		return "PrevoteWait"
	case 6:
		return "Precommit"
	case 7:
		return "PrecommitWait"
	case 8:
		return "Commit"
	}
	return fmt.Sprintf("Step%d", step)
}

// parseBitArray extracts vote counts from a TM2 bit-array string.
// Format: "BA{N:xxx___} M/T = F" — N slots, x=voted, _=not voted.
// Returns the number of 'x' bits, total slots, and whether the fraction > 2/3.
func parseBitArray(s string) (received, total int, maj23 bool) {
	start := strings.Index(s, ":")
	end := strings.Index(s, "}")
	if start < 0 || end < 0 || end <= start {
		return
	}
	bits := s[start+1 : end]
	total = len(bits)
	for _, c := range bits {
		if c == 'x' {
			received++
		}
	}
	// Parse the power fraction "M/T = F" to determine +2/3.
	if eqIdx := strings.Index(s, "= "); eqIdx >= 0 {
		var f float64
		fmt.Sscanf(s[eqIdx+2:], "%f", &f)
		maj23 = f > 0.6667
	}
	return
}

func get(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", url, err)
	}
	return data, nil
}

// parseABCIInfo handles the TM2 JSON-RPC envelope:
// {"result":{"response":{"app_version":"...","last_block_height":"123","last_block_app_hash":"..."}}}
func parseABCIInfo(data []byte) (ABCIInfo, error) {
	var envelope struct {
		Result struct {
			Response map[string]any `json:"response"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ABCIInfo{}, fmt.Errorf("unmarshal abci_info: %w", err)
	}
	r := envelope.Result.Response
	info := ABCIInfo{}
	if v, ok := r["app_version"]; ok {
		info.AppVersion, _ = v.(string)
	}
	info.LastBlockHeight = int64Field(r, "last_block_height")
	if v, ok := r["last_block_app_hash"]; ok {
		info.LastBlockAppHash, _ = v.(string)
	}
	return info, nil
}

// parseBlockResults handles the TM2 JSON-RPC envelope for /block_results:
// {"result":{"height":"123","results":{"deliver_txs":[...]}}}
func parseBlockResults(data []byte, height int64) (BlockResults, error) {
	var envelope struct {
		Result struct {
			Height  any `json:"height"`
			Results struct {
				DeliverTxs []map[string]any `json:"deliver_txs"`
			} `json:"results"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return BlockResults{}, fmt.Errorf("unmarshal block_results: %w", err)
	}

	br := BlockResults{Height: height}
	if h := anyToInt64(envelope.Result.Height); h != 0 {
		br.Height = h
	}
	for _, raw := range envelope.Result.Results.DeliverTxs {
		tx := TxResult{
			GasWanted: int64Field(raw, "gas_wanted"),
			GasUsed:   int64Field(raw, "gas_used"),
		}
		if rb, ok := raw["ResponseBase"]; ok {
			if rbMap, ok := rb.(map[string]any); ok {
				if e, ok := rbMap["Error"]; ok {
					if eMap, ok := e.(map[string]any); ok {
						if msg, ok := eMap["message"].(string); ok {
							tx.Error = msg
						}
					}
				}
			}
		}
		br.DeliverTxs = append(br.DeliverTxs, tx)
	}
	return br, nil
}

func int64Field(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	return anyToInt64(v)
}

func anyToInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		var n int64
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}
