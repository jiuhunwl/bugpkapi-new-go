package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/pkg/masking"
)

type headerFlags []string

func (h *headerFlags) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

type stats struct {
	total        atomic.Int64
	success      atomic.Int64
	failed       atomic.Int64
	latencyTotal atomic.Int64
	latencyMin   atomic.Int64
	latencyMax   atomic.Int64
	// Fixed 1 ms upper-bound buckets; memory does not grow with test duration.
	latencyBuckets [30_001]atomic.Int64
	statusMu       sync.Mutex
	statusCounts   map[int]int64
}

func (s *stats) record(status int, elapsed time.Duration, err error) {
	s.total.Add(1)
	if err == nil && status >= 200 && status < 300 {
		s.success.Add(1)
	} else {
		s.failed.Add(1)
	}

	bucket := (elapsed + time.Millisecond - 1) / time.Millisecond
	if bucket < 0 {
		bucket = 0
	}
	if bucket >= time.Duration(len(s.latencyBuckets)) {
		bucket = time.Duration(len(s.latencyBuckets) - 1)
	}
	s.latencyBuckets[int(bucket)].Add(1)
	elapsedMicros := elapsed.Microseconds()
	s.latencyTotal.Add(elapsedMicros)
	updateMin(&s.latencyMin, elapsedMicros)
	updateMax(&s.latencyMax, elapsedMicros)

	if status > 0 {
		s.statusMu.Lock()
		s.statusCounts[status]++
		s.statusMu.Unlock()
	}
}

// percentile reports a histogram upper bound (1 ms resolution), including errors.
func (s *stats) percentile(percent int) time.Duration {
	total := s.total.Load()
	if total == 0 {
		return 0
	}
	target := (total*int64(percent) + 99) / 100
	var seen int64
	for index := range s.latencyBuckets {
		seen += s.latencyBuckets[index].Load()
		if seen >= target {
			if index == len(s.latencyBuckets)-1 {
				return time.Duration(s.latencyMax.Load()) * time.Microsecond
			}
			return time.Duration(index) * time.Millisecond
		}
	}
	return time.Duration(s.latencyMax.Load()) * time.Microsecond
}

func updateMin(target *atomic.Int64, value int64) {
	for {
		current := target.Load()
		if current != 0 && current <= value {
			return
		}
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func updateMax(target *atomic.Int64, value int64) {
	for {
		current := target.Load()
		if current >= value {
			return
		}
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func main() {
	var headers headerFlags
	url := flag.String("url", "", "target URL")
	duration := flag.Duration("duration", 30*time.Second, "test duration, for example 30s")
	concurrency := flag.Int("concurrency", 100, "number of concurrent workers")
	expectBody := flag.String("expect-body", "", "optional substring that must be present in every response body")
	expectRegex := flag.String("expect-regex", "", "optional regular expression that must match every response body")
	method := flag.String("method", http.MethodGet, "HTTP method")
	flag.Var(&headers, "header", "HTTP header in 'Name: value' format; may be repeated")
	flag.Parse()

	if strings.TrimSpace(*url) == "" {
		exitf("missing -url")
	}
	if *duration <= 0 {
		exitf("-duration must be positive")
	}
	if *concurrency <= 0 {
		exitf("-concurrency must be positive")
	}

	requestHeaders, err := parseHeaders(headers)
	if err != nil {
		exitf("%s", masking.Error(err))
	}
	var expectedPattern *regexp.Regexp
	if strings.TrimSpace(*expectRegex) != "" {
		expectedPattern, err = regexp.Compile(*expectRegex)
		if err != nil {
			exitf("invalid -expect-regex: %v", err)
		}
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DisableCompression:  false,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			MaxConnsPerHost:     *concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	result := &stats{statusCounts: map[int]int64{}}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(*concurrency)
	for i := 0; i < *concurrency; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				runOnce(client, *method, *url, requestHeaders, *expectBody, expectedPattern, result)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	printSummary(result, elapsed)
	if result.total.Load() == 0 || result.failed.Load() > 0 {
		os.Exit(1)
	}
}

func parseHeaders(values []string) (http.Header, error) {
	headers := http.Header{}
	for _, value := range values {
		name, rawValue, ok := strings.Cut(value, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid -header %q, expected 'Name: value'", value)
		}
		headers.Add(strings.TrimSpace(name), strings.TrimSpace(rawValue))
	}
	return headers, nil
}

func runOnce(client *http.Client, method, url string, headers http.Header, expectBody string, expectRegex *regexp.Regexp, result *stats) {
	start := time.Now()
	status := 0
	var err error
	req, reqErr := http.NewRequest(method, url, nil)
	if reqErr != nil {
		result.record(0, time.Since(start), reqErr)
		return
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := client.Do(req)
	if err == nil {
		status = resp.StatusCode
		if expectBody == "" && expectRegex == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
		} else {
			var body []byte
			body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err == nil && !strings.Contains(string(body), expectBody) {
				err = fmt.Errorf("response body does not contain %q", expectBody)
			}
			if err == nil && expectRegex != nil && !expectRegex.Match(body) {
				err = fmt.Errorf("response body does not match %q", expectRegex.String())
			}
		}
		closeErr := resp.Body.Close()
		if err == nil {
			err = closeErr
		}
	}
	result.record(status, time.Since(start), err)
}

func printSummary(result *stats, elapsed time.Duration) {
	total := result.total.Load()
	success := result.success.Load()
	failed := result.failed.Load()
	avgMicros := int64(0)
	if total > 0 {
		avgMicros = result.latencyTotal.Load() / total
	}

	fmt.Println("BUGPK_LOADTEST=SUMMARY")
	fmt.Printf("duration=%s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("requests=%d\n", total)
	fmt.Printf("success=%d\n", success)
	fmt.Printf("failed=%d\n", failed)
	fmt.Printf("rps=%.2f\n", float64(total)/elapsed.Seconds())
	fmt.Printf("latency_avg=%s\n", time.Duration(avgMicros)*time.Microsecond)
	fmt.Printf("latency_min=%s\n", time.Duration(result.latencyMin.Load())*time.Microsecond)
	fmt.Printf("latency_max=%s\n", time.Duration(result.latencyMax.Load())*time.Microsecond)
	fmt.Printf("latency_p50_upper=%s\n", result.percentile(50))
	fmt.Printf("latency_p95_upper=%s\n", result.percentile(95))
	fmt.Printf("latency_p99_upper=%s\n", result.percentile(99))

	result.statusMu.Lock()
	defer result.statusMu.Unlock()
	statuses := make([]int, 0, len(result.statusCounts))
	for status := range result.statusCounts {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	for _, status := range statuses {
		fmt.Printf("status_%d=%d\n", status, result.statusCounts[status])
	}
}

func exitf(format string, args ...any) {
	// Keep load-test failures consistent with service and other diagnostic
	// CLIs: an HTTP/client error may contain the target URL or credentials.
	message := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, masking.ErrorText(message))
	os.Exit(2)
}
