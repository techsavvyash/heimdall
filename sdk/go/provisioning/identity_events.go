package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type IdentityEventReaderOptions struct {
	Issuer, OrganizationID, Token, TransportProfile string
	PageSize                                        int // Defaults to 100.
}

type IdentityEvent struct {
	OrganizationID string `json:"organizationId"`
	AggregateType  string `json:"aggregateType"`
	AggregateID    string `json:"aggregateId"`
	Type           string `json:"type"`
	Sequence       string `json:"sequence"`
	CreationDate   string `json:"creationDate"`
}

type IdentityEventWindow struct {
	Events   []IdentityEvent `json:"events"`
	Requests int             `json:"requests"`
}

type IdentityEventReader struct {
	options IdentityEventReaderOptions
	client  *http.Client
}

var identityEventType = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var identityEventTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)

func eventMicros(value string) (int64, bool) {
	if !identityEventTime.MatchString(value) {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	return t.UnixMicro(), err == nil && t.Unix() >= 0 && t.Nanosecond()%1000 == 0
}

func formatEventMicros(value int64) string {
	return time.UnixMicro(value).UTC().Format("2006-01-02T15:04:05.000000Z")
}

// NewIdentityEventReader binds a historical event scan to a configured ZITADEL organization.
func NewIdentityEventReader(o IdentityEventReaderOptions) (*IdentityEventReader, error) {
	if o.TransportProfile == "" {
		o.TransportProfile = "production"
	}
	if o.PageSize == 0 {
		o.PageSize = 100
	}
	if contract.ValidateEndpoint(o.Issuer, o.TransportProfile) != nil ||
		!providerID.MatchString(o.OrganizationID) || len(o.Token) > 4096 || !providerToken.MatchString(o.Token) ||
		o.PageSize < 1 || o.PageSize > 100 {
		return nil, contract.ErrInvalidArgument
	}
	origin, _ := url.Parse(o.Issuer)
	if origin.Path != "" && origin.Path != "/" {
		return nil, contract.ErrInvalidArgument
	}
	return &IdentityEventReader{o, &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type providerIdentityEvent struct {
	Aggregate struct {
		ID, ResourceOwner string
		Type              struct{ Type string }
	}
	Type                   struct{ Type string }
	CreationDate, Sequence string
}

type eventRange struct{ lower, upper int64 }
type eventKey struct{ aggregateType, aggregateID, sequence string }

func (r *IdentityEventReader) readPage(ctx context.Context, window eventRange) ([]providerIdentityEvent, error) {
	body := map[string]any{
		"resourceOwner": r.options.OrganizationID,
		"asc":           true,
		"limit":         r.options.PageSize,
		"range":         map[string]string{"since": formatEventMicros(window.lower), "until": formatEventMicros(window.upper)},
	}
	var response map[string]json.RawMessage
	found, err := providerCall(ctx, r.client, r.options.Issuer, r.options.Token, "/admin/v1/events/_search", body, &response, false)
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		return nil, cancelled
	}
	var throttled *contract.ProviderThrottled
	if errors.As(err, &throttled) {
		return nil, contract.ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if found && len(response) == 0 {
		// ZITADEL omits an empty protobuf repeated field from JSON.
		return nil, nil
	}
	var events []providerIdentityEvent
	if !found || json.Unmarshal(response["events"], &events) != nil || events == nil || len(events) > r.options.PageSize {
		return nil, contract.ErrUnavailable
	}
	return events, nil
}

func parseIdentityEvent(raw providerIdentityEvent, organizationID string, window eventRange) (IdentityEvent, error) {
	micros, validTime := eventMicros(raw.CreationDate)
	sequence, err := strconv.ParseUint(raw.Sequence, 10, 64)
	if !providerID.MatchString(raw.Aggregate.ResourceOwner) || !providerID.MatchString(raw.Aggregate.ID) ||
		!identityEventType.MatchString(raw.Aggregate.Type.Type) || !identityEventType.MatchString(raw.Type.Type) ||
		err != nil || sequence == 0 || strconv.FormatUint(sequence, 10) != raw.Sequence ||
		!validTime || micros <= window.lower || micros >= window.upper {
		return IdentityEvent{}, contract.ErrUnavailable
	}
	if raw.Aggregate.ResourceOwner != organizationID {
		return IdentityEvent{}, contract.ErrConflict
	}
	return IdentityEvent{organizationID, raw.Aggregate.Type.Type, raw.Aggregate.ID, raw.Type.Type,
		raw.Sequence, formatEventMicros(micros)}, nil
}

// ReadWindow scans a fixed range with exclusive UTC bounds and returns only validated metadata.
func (r *IdentityEventReader) ReadWindow(ctx context.Context, since, until string) (IdentityEventWindow, error) {
	lower, lowerOK := eventMicros(since)
	upper, upperOK := eventMicros(until)
	empty := IdentityEventWindow{}
	if r == nil || r.client == nil || ctx == nil || !lowerOK || !upperOK || lower >= upper {
		return empty, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	type observation struct {
		event     IdentityEvent
		confirmed bool
	}
	observed := make(map[eventKey]observation)
	pending := []eventRange{{lower, upper}}
	requests := 0
	for len(pending) > 0 {
		if err := contract.CheckCancellation(ctx); err != nil {
			return empty, err
		}
		requests++
		if requests > 128 {
			return empty, contract.ErrUnavailable
		}
		window := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		page, err := r.readPage(ctx, window)
		if err != nil {
			return empty, err
		}
		complete := len(page) < r.options.PageSize
		pageKeys := make(map[eventKey]bool, len(page))
		for _, raw := range page {
			event, err := parseIdentityEvent(raw, r.options.OrganizationID, window)
			if err != nil {
				return empty, err
			}
			key := eventKey{event.AggregateType, event.AggregateID, event.Sequence}
			if pageKeys[key] {
				return empty, contract.ErrUnavailable
			}
			pageKeys[key] = true
			previous, exists := observed[key]
			if exists && previous.event != event {
				return empty, contract.ErrConflict
			}
			observed[key] = observation{event, complete || previous.confirmed}
		}
		if !complete {
			if window.upper-window.lower <= 2 {
				return empty, contract.ErrUnavailable
			}
			middle := (window.lower + window.upper) / 2
			// Both bounds are exclusive. Include the midpoint in the left subwindow.
			pending = append(pending, eventRange{middle, window.upper}, eventRange{window.lower, middle + 1})
		}
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	events := make([]IdentityEvent, 0, len(observed))
	for _, entry := range observed {
		if !entry.confirmed {
			return empty, contract.ErrUnavailable
		}
		events = append(events, entry.event)
	}
	slices.SortFunc(events, func(a, b IdentityEvent) int {
		if order := strings.Compare(a.CreationDate, b.CreationDate); order != 0 {
			return order
		}
		if order := strings.Compare(a.AggregateType, b.AggregateType); order != 0 {
			return order
		}
		if order := strings.Compare(a.AggregateID, b.AggregateID); order != 0 {
			return order
		}
		if len(a.Sequence) != len(b.Sequence) {
			return len(a.Sequence) - len(b.Sequence)
		}
		return strings.Compare(a.Sequence, b.Sequence)
	})
	return IdentityEventWindow{events, requests}, nil
}
