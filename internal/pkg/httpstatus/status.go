// Package httpstatus centralizes HTTP status classification used by call logs
// and success-rate statistics.
package httpstatus

// IsSuccess reports whether status belongs to the HTTP 2xx success class.
// Redirects and client/server errors are intentionally not counted as
// successful API calls.
func IsSuccess(status int) bool {
	return status >= 200 && status < 300
}
