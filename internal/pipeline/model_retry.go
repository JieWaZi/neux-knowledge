package pipeline

import (
	"context"
	"strings"
)

var rateLimitErrorIndicators = []string{
	"qpm",        // Gateway qpm quota (0x04030020).
	"qps",        // Gateway qps quota.
	"rate limit", // OpenAI-style "rate limit reached"
	"rate_limit",
	"too many requests", // RFC 6585 language
	"throttl",           // "throttled"
	"调用频率",              // Common wording used by Chinese gateways.
	"频率超限",
	"请求过于频繁",
	"繁忙", // "Service busy, please try again later".
	"try again later",
	"retry later",
	"slow down",
}

func isTransientLLMError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	// Never retry after the parent ctx itself expired — the task is
	// being cancelled and the next attempt would just fail again.
	if ctx.Err() != nil {
		return false
	}

	msg := err.Error()
	// Providers that bubble HTTP status up formatted as
	// "API request failed with status NNN: ..." — match that first.
	for _, s := range []string{
		"status 408", "status 429",
		"status 500", "status 501", "status 502", "status 503", "status 504",
		"status 520", "status 521", "status 522", "status 523", "status 524",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}

	lower := strings.ToLower(msg)
	// Some gateways report QPM/QPS throttling as HTTP 403 instead of 429
	// (e.g. a MaaS gateway returning code 0x04030020, message
	// "Call frequency (qpm) exceeded"). A plain 403 is usually an authorization
	// failure and must NOT be retried, so this stays gated on rate-limit
	// indicators in the response body, which provider errors embed:
	// "API request failed with status 403: {"code":0x04030020,...}".
	if strings.Contains(msg, "status 403") {
		for _, s := range rateLimitErrorIndicators {
			if strings.Contains(lower, s) {
				return true
			}
		}
	}

	for _, s := range []string{
		"timeout",
		"timed out",
		"connection reset",
		"connection refused",
		"broken pipe",
		"no such host", // DNS hiccup
		"i/o timeout",
		"unexpected eof",
		"tls handshake",
		"context deadline exceeded", // nested per-call deadline
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func isRateLimitedGeneration(message string) bool {
	if strings.Contains(message, "status 429") {
		return true
	}
	if strings.Contains(message, "status 403") {
		lower := strings.ToLower(message)
		for _, word := range rateLimitErrorIndicators {
			if strings.Contains(lower, word) {
				return true
			}
		}
	}
	return false
}
