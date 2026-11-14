package identity

import (
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"regexp"
	"strconv"
	"strings"
)

var sessionKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var sessionRevision = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

type SessionContext struct{ Key, Revision string }

// SessionIdentityContext takes a loaded internal storage key/revision, never a browser cookie.
func SessionIdentityContext(key, revision string) (string, error) {
	if !sessionKey.MatchString(key) || !sessionRevision.MatchString(revision) {
		return "", contract.ErrInvalidArgument
	}
	if _, e := strconv.ParseInt(revision, 10, 64); e != nil {
		return "", contract.ErrInvalidArgument
	}
	return "session:" + key + ":" + revision, nil
}
func ParseIdentityContext(value string) (*SessionContext, error) {
	if value == "bearer" {
		return nil, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "session" {
		return nil, contract.ErrInvalidArgument
	}
	expected, e := SessionIdentityContext(parts[1], parts[2])
	if e != nil || expected != value {
		return nil, contract.ErrInvalidArgument
	}
	return &SessionContext{parts[1], parts[2]}, nil
}
