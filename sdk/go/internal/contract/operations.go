package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}
type Intent struct {
	SchemaVersion   int      `json:"schemaVersion"`
	ProductID       string   `json:"productId"`
	Environment     string   `json:"environment"`
	Kind            string   `json:"kind"`
	Actor           Identity `json:"actor"`
	Target          Identity `json:"target"`
	WorkspaceID     string   `json:"workspaceId"`
	Name            string   `json:"name"`
	Roles           []string `json:"roles"`
	ManifestVersion int      `json:"manifestVersion"`
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

var intentToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var intentRole = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func intentID(s string, max int, empty bool) bool {
	return (empty && s == "") || len(s) > 0 && len(s) <= max && intentToken.MatchString(s)
}

// ValidateIntent covers shape and semantics, but not authorization or product role policy.
func ValidateIntent(i Intent) error {
	if i.SchemaVersion != 1 || !intentID(i.ProductID, 64, false) || !intentID(i.Environment, 64, false) || !intentID(i.WorkspaceID, 128, true) || i.ManifestVersion < 1 || i.ManifestVersion > 2147483647 || i.Roles == nil || len(i.Roles) > 128 || utf8.RuneCountInString(i.Name) > 256 {
		return ErrInvalidArgument
	}
	switch i.Kind {
	case "create_workspace", "add_member", "remove_member", "assign_roles", "transfer_owner":
	default:
		return ErrInvalidArgument
	}
	seen := map[string]bool{}
	for _, role := range i.Roles {
		if len(role) > 64 || !intentRole.MatchString(role) || seen[role] {
			return ErrInvalidArgument
		}
		seen[role] = true
	}
	actor, err := EncodeIdentity(i.Actor.Issuer, i.Actor.Subject)
	if err != nil {
		return err
	}
	target, err := EncodeIdentity(i.Target.Issuer, i.Target.Subject)
	if err != nil {
		return err
	}
	if strings.ContainsFunc(i.Name, func(r rune) bool { return r < 32 || r == 127 }) || !utf8.ValidString(i.Name) || len(i.Name) > 1024 {
		return ErrInvalidArgument
	}
	if i.Kind == "create_workspace" {
		if i.WorkspaceID != "" || strings.TrimFunc(i.Name, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff }) == "" || len(i.Roles) != 0 || actor != target {
			return ErrInvalidArgument
		}
	} else if i.WorkspaceID == "" || i.Name != "" || ((i.Kind == "remove_member" || i.Kind == "transfer_owner") && len(i.Roles) != 0) {
		return ErrInvalidArgument
	}

	return nil
}

// ParseIntent validates bounded Unicode JSON and exact fields before typed decoding.
func ParseIntent(raw []byte) (Intent, error) {
	empty := Intent{}
	if len(raw) > 1048576 || !ValidJSONUnicode(raw) {
		return empty, ErrInvalidArgument
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return empty, ErrInvalidArgument
	}
	root, ok := auditFields(value, []string{"schemaVersion", "productId", "environment", "kind", "actor", "target", "workspaceId", "name", "roles", "manifestVersion"}, nil)
	if !ok {
		return empty, ErrInvalidArgument
	}
	for _, key := range []string{"productId", "environment", "kind", "workspaceId", "name"} {
		if _, ok := root[key].(string); !ok {
			return empty, ErrInvalidArgument
		}
	}
	for _, key := range []string{"actor", "target"} {
		m, ok := auditFields(root[key], []string{"issuer", "subject"}, nil)
		if !ok {
			return empty, ErrInvalidArgument
		}
		for _, k := range []string{"issuer", "subject"} {
			if _, ok := m[k].(string); !ok {
				return empty, ErrInvalidArgument
			}
		}
	}
	if _, ok := root["roles"].([]any); !ok {
		return empty, ErrInvalidArgument
	}
	for _, key := range []string{"schemaVersion", "manifestVersion"} {
		number, ok := root[key].(json.Number)
		if !ok {
			return empty, ErrInvalidArgument
		}
		n, e := number.Float64()
		if e != nil || n < 1 || n > 2147483647 || math.Trunc(n) != n {
			return empty, ErrInvalidArgument
		}
		root[key] = n
	}
	// Normalize decoded JSON first so duplicate members have the same last-value semantics as TS.
	normalized, e := json.Marshal(root)
	if e != nil {
		return empty, ErrInvalidArgument
	}
	var intent Intent
	if json.Unmarshal(normalized, &intent) != nil || ValidateIntent(intent) != nil {
		return empty, ErrInvalidArgument
	}
	return intent, nil
}

// IntentDigests always validates before hashing and never authorizes the intent.
func IntentDigests(i Intent, idempotencyKey string) (IntentDigest, error) {
	if err := ValidateIntent(i); err != nil {
		return IntentDigest{}, err
	}
	if !utf8.ValidString(idempotencyKey) || len(idempotencyKey) == 0 || len(idempotencyKey) > 256 {
		return IntentDigest{}, ErrInvalidArgument
	}
	actor, _ := EncodeIdentity(i.Actor.Issuer, i.Actor.Subject)
	target, _ := EncodeIdentity(i.Target.Issuer, i.Target.Subject)
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

// IntentBinding describes indexed journal columns, not authenticated provenance.
type IntentBinding struct {
	ProductID, Environment, WorkspaceID, Kind string
	ActorHash, TargetHash, Fingerprint        string
	ManifestVersion                           int
}

// ParseBoundIntent rejects corrupt payload/index pairs without treating them as absent.
func ParseBoundIntent(raw []byte, b IntentBinding) (Intent, error) {
	i, e := ParseIntent(raw)
	if e != nil {
		return Intent{}, ErrUnavailable
	}
	actor, _ := EncodeIdentity(i.Actor.Issuer, i.Actor.Subject)
	target, _ := EncodeIdentity(i.Target.Issuer, i.Target.Subject)
	d, _ := IntentDigests(i, "binding-check")
	if !intentID(b.WorkspaceID, 128, false) || i.ProductID != b.ProductID || i.Environment != b.Environment || i.Kind != b.Kind || i.ManifestVersion != b.ManifestVersion || (i.Kind != "create_workspace" && i.WorkspaceID != b.WorkspaceID) || sha(actor) != b.ActorHash || sha(target) != b.TargetHash || d.Fingerprint != b.Fingerprint {
		return Intent{}, ErrUnavailable
	}
	return i, nil
}
