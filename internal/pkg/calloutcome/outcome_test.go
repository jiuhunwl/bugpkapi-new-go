package calloutcome

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		success bool
		status  int
		want    Class
	}{
		{"2xx success", true, 200, Success},
		{"2xx success with business failure", true, 200, Success},
		{"404 expected empty", false, 404, ExpectedEmpty},
		{"429 throttled", false, 429, Throttle},
		{"400 client error", false, 400, ClientError},
		{"401 client error", false, 401, ClientError},
		{"403 client error", false, 403, ClientError},
		{"422 client error", false, 422, ClientError},
		{"500 upstream failure", false, 500, UpstreamError},
		{"503 upstream failure", false, 503, UpstreamError},
		{"network failure without status", false, 0, UpstreamError},
		{"2xx flagged as failure is an upstream failure", false, 200, UpstreamError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.success, tc.status); got != tc.want {
				t.Fatalf("Classify(%v, %d) = %d, want %d", tc.success, tc.status, got, tc.want)
			}
		})
	}
}
