package contract

import (
	"net/http"
	"strconv"
	"time"
)

// ProviderThrottled carries a bounded Retry-After hint for pre-dispatch reads.
type ProviderThrottled struct{ Delay time.Duration }

func (*ProviderThrottled) Error() string { return ErrUnavailable.Error() }
func (*ProviderThrottled) Unwrap() error { return ErrUnavailable }

func Throttled(header string) error {
	var delay time.Duration
	if seconds, err := strconv.ParseUint(header, 10, 64); err == nil {
		delay = time.Duration(min(seconds, 86400)) * time.Second
	} else if at, err := http.ParseTime(header); err == nil {
		delay = min(time.Until(at), 24*time.Hour)
	}
	return &ProviderThrottled{max(0, delay)}
}
