package postgres

import (
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type IdentityCoveragePolicy struct {
	MaxWriterTransactionMs, MaxClockOffsetMs, MinimumRetentionMs int64
}

// DeriveIdentityEventCoverage derives bounds from operator-verified limits for the pinned provider deployment.
func DeriveIdentityEventCoverage(policy IdentityCoveragePolicy, now time.Time) (IdentityEventCoverage, error) {
	const lifetimeMs int64 = 30000
	const maxSafe int64 = 9007199254740991
	nowMs := now.UnixMilli()
	writer, skew, retention := policy.MaxWriterTransactionMs, policy.MaxClockOffsetMs, policy.MinimumRetentionMs
	if writer < 1 || writer > maxSafe || skew < 0 || skew > maxSafe ||
		retention < 1 || retention > maxSafe || nowMs < 1 || nowMs > 253402300799999-lifetimeMs {
		return IdentityEventCoverage{}, contract.ErrInvalidArgument
	}
	visibleBefore := nowMs - writer - 3*skew
	retentionFloor := nowMs + lifetimeMs + skew - retention
	if visibleBefore <= 0 || visibleBefore <= retentionFloor {
		return IdentityEventCoverage{}, contract.ErrUnavailable
	}
	return IdentityEventCoverage{
		RetainedFromMicros:  max(0, retentionFloor) * 1000,
		VisibleBeforeMicros: visibleBefore * 1000,
		ValidUntilMs:        nowMs + lifetimeMs,
	}, nil
}
