package api

// Internals the external tests check: the upstream policy and what counts as
// a failed response, which the wiring hands to resiliencehttp.
var (
	UpstreamPolicy = upstreamPolicy
	FailedResponse = failedResponse
)

// RememberSpan is the span middleware, checked on its own with hand-made
// span contexts (sampled and not).
var RememberSpan = rememberSpan
