package payment

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

const (
	trongridBaseURL  = "https://api.trongrid.io"
	trongridPageSize = "200"
	trongridMaxPages = 20
)

// Confirmations=1 means the provider's confirmed state, not a measured block depth.
type Trc20Transfer struct {
	ReceiptID      int64
	TxID           string
	From           string
	To             string
	Amount         string
	BlockTimestamp int64
	Confirmations  int
}

type Trc20ChainClient interface {
	ListTransfers(context.Context, string, string, string, int64) ([]Trc20Transfer, error)
}

type trongridClient struct {
	baseURL    string
	httpClient *http.Client
}

func newTrongridClient() *trongridClient {
	return &trongridClient{baseURL: trongridBaseURL, httpClient: httpclient.Outbound(15 * time.Second)}
}

type trongridTransfersResponse struct {
	Data []struct {
		TransactionID  string `json:"transaction_id"`
		BlockTimestamp int64  `json:"block_timestamp"`
		From           string `json:"from"`
		To             string `json:"to"`
		Type           string `json:"type"`
		Value          string `json:"value"`
		TokenInfo      struct {
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
		} `json:"token_info"`
	} `json:"data"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Meta    struct {
		Fingerprint string `json:"fingerprint"`
		Links       struct {
			Next string `json:"next"`
		} `json:"links"`
	} `json:"meta"`
}

// ListTransfers reads a fixed window in chronological order. Any incomplete
// page fails the entire read; callers must never advance a cursor on partial data.
var errTrongridPageLimit = errors.New("trongrid scan window exceeds page limit")

func (c *trongridClient) ListTransfers(ctx context.Context, address, contract, apiKey string, minTimestampMS int64) ([]Trc20Transfer, error) {
	lower, upper := minTimestampMS, time.Now().UnixMilli()
	if lower <= 0 {
		lower = upper - trc20ScanWindow.Milliseconds()
	}
	failedUpper := int64(0)
	emptyUpper := lower
	for {
		transfers, err := c.listTransferWindow(ctx, address, contract, apiKey, lower, upper)
		if !errors.Is(err, errTrongridPageLimit) {
			if err != nil {
				return nil, err
			}
			progressed := false
			for _, transfer := range transfers {
				if transfer.BlockTimestamp > lower {
					progressed = true
					break
				}
			}
			if failedUpper > 0 && !progressed {
				if failedUpper-upper <= 1 {
					return nil, fmt.Errorf("confirmed TRC20 backlog cannot advance within a bounded window")
				}
				emptyUpper = upper
				upper += (failedUpper - upper) / 2
				continue
			}
			return transfers, nil
		}
		if upper <= emptyUpper {
			return nil, fmt.Errorf("a single millisecond exceeds the safe TRC20 page limit")
		}
		failedUpper = upper
		upper = emptyUpper + (upper-emptyUpper)/2
	}
}

func (c *trongridClient) listTransferWindow(ctx context.Context, address, contract, apiKey string, minTimestampMS, upper int64) ([]Trc20Transfer, error) {
	address = strings.TrimSpace(address)
	contract = strings.TrimSpace(contract)
	query := url.Values{"only_to": {"true"}, "only_confirmed": {"true"}, "limit": {trongridPageSize}, "contract_address": {contract}, "order_by": {"block_timestamp,asc"}, "max_timestamp": {strconv.FormatInt(upper, 10)}}
	if minTimestampMS > 0 {
		query.Set("min_timestamp", strconv.FormatInt(minTimestampMS, 10))
	}
	var transfers []Trc20Transfer
	seenPages := make(map[string]bool)
	for page := 0; page < trongridMaxPages; page++ {
		// Use only the opaque fingerprint. Provider-supplied links cannot change origin.
		endpoint := c.baseURL + "/v1/accounts/" + url.PathEscape(address) + "/transactions/trc20?" + query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if key := strings.TrimSpace(apiKey); key != "" {
			request.Header.Set("TRON-PRO-API-KEY", key)
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			return nil, err
		}
		var payload trongridTransfersResponse
		if response.StatusCode == http.StatusOK {
			err = httpclient.DecodeJSON(response.Body, httpclient.MaxPaymentResponseBytes, &payload)
		} else {
			err = fmt.Errorf("trongrid returned status %d", response.StatusCode)
		}
		_ = response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read trongrid page: %w", err)
		}
		if !payload.Success {
			return nil, fmt.Errorf("trongrid did not confirm query success")
		}
		for _, item := range payload.Data {
			if item.Type != "Transfer" {
				continue
			}
			if item.TokenInfo.Address != contract || strings.TrimSpace(item.To) != address {
				return nil, fmt.Errorf("trongrid transfer recipient or contract mismatch")
			}
			txid := strings.ToLower(strings.TrimSpace(item.TransactionID))
			if !trc20TxIDPattern.MatchString(txid) || item.BlockTimestamp <= 0 || item.BlockTimestamp > upper || item.TokenInfo.Decimals != 6 {
				return nil, fmt.Errorf("invalid confirmed TRC20 transfer")
			}
			amount, err := trc20TokenAmount(item.Value, item.TokenInfo.Decimals)
			if err != nil {
				return nil, err
			}
			transfers = append(transfers, Trc20Transfer{TxID: txid, From: strings.TrimSpace(item.From), To: address, Amount: amount, BlockTimestamp: item.BlockTimestamp, Confirmations: trc20DefaultConfirmations})
		}
		if len(payload.Data) == 0 {
			return transfers, nil
		}
		next := strings.TrimSpace(payload.Meta.Fingerprint)
		if next == "" {
			if payload.Meta.Links.Next != "" {
				return nil, fmt.Errorf("trongrid pagination token missing")
			}
			return transfers, nil
		}
		if len(next) > 2048 || seenPages[next] {
			return nil, fmt.Errorf("invalid or repeated trongrid pagination token")
		}
		seenPages[next] = true
		query.Set("fingerprint", next)
	}
	return nil, errTrongridPageLimit
}

func trc20TokenAmount(value string, decimals int) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 78 || decimals < 0 || decimals > 18 {
		return "", fmt.Errorf("invalid TRC20 amount encoding")
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", fmt.Errorf("invalid TRC20 amount encoding")
		}
	}
	units, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return "", fmt.Errorf("invalid TRC20 amount encoding")
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Rat).SetFrac(units, divisor).FloatString(6), nil
}
