// Package calloutcome classifies one gateway call into a diagnosis bucket.
//
// The bucket is persisted with the call log and rolled up per minute so every
// failure-rate surface can share one definition:
//
//   - only UpstreamError counts against the API service's availability;
//   - ClientError and Throttle stay visible but never blame the upstream;
//   - ExpectedEmpty covers "resource not found" outcomes such as HTTP 404 and
//     configured business stop codes, which are normal results rather than
//     failures.
package calloutcome

// Class is the persisted outcome bucket. Zero means "not classified" and is
// only produced by rows written before migration 000110; every new row writes
// an explicit class.
type Class uint8

const (
	Unknown Class = iota
	Success
	ExpectedEmpty
	ClientError
	Throttle
	UpstreamError
)

// Classify derives a bucket from the gateway's final success flag and the
// response status code. 4xx (except 404 and 429) is attributed to the caller;
// everything else that is not a 2xx success is an upstream/service failure.
func Classify(success bool, status int) Class {
	if success {
		return Success
	}
	switch {
	case status == 404:
		return ExpectedEmpty
	case status == 429:
		return Throttle
	case status >= 400 && status < 500:
		return ClientError
	default:
		return UpstreamError
	}
}
