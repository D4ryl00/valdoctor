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
