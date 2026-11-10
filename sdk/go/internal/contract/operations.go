package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Identity struct{ Issuer, Subject string }
type Intent struct {
	SchemaVersion                int
	ProductID, Environment, Kind string
	Actor, Target                Identity
	WorkspaceID, Name            string
	Roles                        []string
	ManifestVersion              int
}
type IntentDigest struct{ Fingerprint, KeyDigest string }

func stringArray(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = quoteIdentity(item)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
func sha(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }

// IntentDigests follows structural validation and does not authorize the intent.
func IntentDigests(i Intent, idempotencyKey string) (IntentDigest, error) {
	actor, err := EncodeIdentity(i.Actor.Issuer, i.Actor.Subject)
	if err != nil {
		return IntentDigest{}, err
	}
	target, err := EncodeIdentity(i.Target.Issuer, i.Target.Subject)
	if err != nil {
		return IntentDigest{}, err
	}
	if strings.ContainsFunc(i.Name, func(r rune) bool { return r < 32 || r == 127 }) || !utf8.ValidString(i.Name) || len(i.Name) > 1024 || !utf8.ValidString(idempotencyKey) || len(idempotencyKey) == 0 || len(idempotencyKey) > 256 {
		return IntentDigest{}, ErrInvalidArgument
	}
	if i.Kind == "create_workspace" {
		if i.WorkspaceID != "" || strings.TrimFunc(i.Name, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff }) == "" || len(i.Roles) != 0 || actor != target {
			return IntentDigest{}, ErrInvalidArgument
		}
	} else if i.WorkspaceID == "" || i.Name != "" || ((i.Kind == "remove_member" || i.Kind == "transfer_owner") && len(i.Roles) != 0) {
		return IntentDigest{}, ErrInvalidArgument
	}
	roles := append([]string(nil), i.Roles...)
	// Schema role identifiers are ASCII, so byte and UTF-16 ordering coincide.
	sort.Strings(roles)
	fields := []string{"heimdall.intent.v1", i.ProductID, i.Environment, i.Kind, actor, target, i.WorkspaceID, i.Name}
	prefix := stringArray(fields)
	canonical := prefix[:len(prefix)-1] + "," + stringArray(roles) + "," + strconv.Itoa(i.ManifestVersion) + "]"
	keyScope := stringArray([]string{"heimdall.idempotency.v1", i.ProductID, i.Environment, i.Kind, actor, idempotencyKey})
	return IntentDigest{sha(canonical), sha(keyScope)}, nil
}

var operationTransitions = map[string]map[string]string{
	"pending":     {"dispatch": "dispatching", "pre_dispatch_failure": "retryable", "cancel": "cancelled"},
	"retryable":   {"retry": "pending", "cancel": "cancelled"},
	"dispatching": {"confirm": "confirmed", "uncertain": "uncertain", "rejected": "retryable"},
	"confirmed":   {"complete": "completed"},
}

// NextOperationState requires a subsequent database generation CAS to commit.
func NextOperationState(state, event string, dispatchCount, generation, expectedGeneration int64) (string, error) {
	if dispatchCount < 0 || generation < 1 || expectedGeneration < 1 || dispatchCount > 9007199254740991 || generation > 9007199254740991 || expectedGeneration > 9007199254740991 {
		return "", ErrInvalidArgument
	}
	if generation != expectedGeneration || (event == "cancel" && dispatchCount != 0) {
		return "", ErrConflict
	}
	next, ok := operationTransitions[state][event]
	if !ok {
		return "", ErrConflict
	}
	return next, nil
}
