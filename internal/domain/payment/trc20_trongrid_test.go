package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func trc20PageTransfer(txid string, ts int64) map[string]any {
	return map[string]any{"transaction_id": txid, "block_timestamp": ts, "from": "TBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "to": "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "type": "Transfer", "value": "5010000", "token_info": map[string]any{"address": trc20USDTContract, "decimals": 6}}
}
func TestTrongridTraversesPagesWithoutFollowingProviderURLs(t *testing.T) {
	calls := 0
	maxTimestamp := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("order_by") != "block_timestamp,asc" {
			t.Errorf("missing ascending order: %s", q.Get("order_by"))
		}
		if q.Get("max_timestamp") == "" {
			t.Error("pagination lacks a fixed upper bound")
		}
		if calls == 1 {
			maxTimestamp = q.Get("max_timestamp")
		} else if maxTimestamp != q.Get("max_timestamp") {
			t.Error("snapshot changed between pages")
		}
		if q.Get("only_confirmed") != "true" || q.Get("contract_address") != trc20USDTContract {
			t.Error("lost provider filters")
		}
		page := map[string]any{"success": true, "data": []any{trc20PageTransfer(strings.Repeat("a", 64), 1000)}}
		if calls == 1 {
			page["meta"] = map[string]any{"fingerprint": "next-token", "links": map[string]any{"next": "https://untrusted.example/ignore"}}
		} else {
			if q.Get("fingerprint") != "next-token" {
				t.Error("missing pagination token")
			}
			page["data"] = []any{trc20PageTransfer(strings.Repeat("b", 64), 2000)}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := &trongridClient{baseURL: server.URL, httpClient: server.Client()}
	transfers, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(transfers) != 2 {
		t.Fatalf("calls=%d transfers=%d; want all 2 pages", calls, len(transfers))
	}
}
func TestTrongridRejectsFailureAndAmbiguousJSON(t *testing.T) {
	for _, body := range []string{`{"success":false,"data":[]}`, `{"success":true,"success":false,"data":[]}`, `{"success":true,"data":[]} {"unexpected":true}`} {
		t.Run(fmt.Sprintf("%d", len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client := &trongridClient{baseURL: server.URL, httpClient: server.Client()}
			if _, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", 0); err == nil {
				t.Fatal("accepted failed or ambiguous provider response")
			}
		})
	}
}
func TestTrongridPaginationCycleReturnsNoPartialTransfers(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{trc20PageTransfer(strings.Repeat("a", 64), 1000)}, "meta": map[string]any{"fingerprint": "repeated"}})
	}))
	defer server.Close()
	client := &trongridClient{baseURL: server.URL, httpClient: server.Client()}
	transfers, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", 0)
	if err == nil || len(transfers) != 0 {
		t.Fatalf("pagination cycle returned partial success: transfers=%d err=%v", len(transfers), err)
	}
	if calls > 3 {
		t.Fatalf("unbounded pagination: calls=%d", calls)
	}
}

func TestTrongridLargeBacklogShrinksToCompleteNonemptyWindow(t *testing.T) {
	const total = 4001
	base := time.Now().Add(-time.Hour).UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		lower, _ := strconv.ParseInt(query.Get("min_timestamp"), 10, 64)
		upper, _ := strconv.ParseInt(query.Get("max_timestamp"), 10, 64)
		start := 1
		if lower > base {
			start = int(lower - base)
		}
		if cursor := query.Get("fingerprint"); cursor != "" {
			start, _ = strconv.Atoi(cursor)
		}
		end := total
		if upper-base < int64(end) {
			end = int(upper - base)
		}
		stop := start + 200
		if stop > end+1 {
			stop = end + 1
		}
		data := []any{}
		for i := start; i < stop; i++ {
			data = append(data, trc20PageTransfer(fmt.Sprintf("%064x", i), base+int64(i)))
		}
		page := map[string]any{"success": true, "data": data}
		if stop <= end {
			page["meta"] = map[string]any{"fingerprint": strconv.Itoa(stop)}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := &trongridClient{baseURL: server.URL, httpClient: server.Client()}
	first, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", base)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || len(first) >= total {
		t.Fatalf("expected a complete smaller nonempty window, got %d", len(first))
	}
	seen := map[string]bool{}
	for _, tr := range first {
		seen[tr.TxID] = true
	}
	second, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", first[len(first)-1].BlockTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range second {
		seen[tr.TxID] = true
	}
	if len(seen) != total {
		t.Fatalf("backlog coverage=%d, want %d", len(seen), total)
	}
}

func TestTrongridLargeBacklogAfterQuietGapStillMakesProgress(t *testing.T) {
	const total = 4001
	lower := time.Now().Add(-2 * time.Hour).UnixMilli()
	burst := lower + int64((90*time.Minute)/time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		upper, _ := strconv.ParseInt(q.Get("max_timestamp"), 10, 64)
		offset := 0
		if q.Get("fingerprint") != "" {
			offset, _ = strconv.Atoi(q.Get("fingerprint"))
		}
		available := 0
		if upper >= burst {
			available = int(upper-burst) + 1
			if available > total {
				available = total
			}
		}
		stop := offset + 200
		if stop > available {
			stop = available
		}
		data := []any{}
		for i := offset; i < stop; i++ {
			data = append(data, trc20PageTransfer(fmt.Sprintf("%064x", i+1), burst+int64(i)))
		}
		page := map[string]any{"success": true, "data": data}
		if stop < available {
			page["meta"] = map[string]any{"fingerprint": strconv.Itoa(stop)}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := &trongridClient{baseURL: server.URL, httpClient: server.Client()}
	transfers, err := client.ListTransfers(context.Background(), "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", trc20USDTContract, "", lower)
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) == 0 || transfers[len(transfers)-1].BlockTimestamp <= lower {
		t.Fatal("shrunk scan returned success without any cursor progress despite known backlog")
	}
}
