package provisioning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type DirectoryReaderOptions struct {
	Issuer, OrganizationID, Token, TransportProfile string
}

type DirectoryUser struct {
	ID         string `json:"id"`
	ExternalID string `json:"externalId,omitempty"`
}

type DirectoryPage struct {
	Users          []DirectoryUser
	NextStartIndex int64 // Zero means the end of this scan.
}

type DirectoryIdentityState struct {
	State      string
	ExternalID string
}

type DirectoryReader struct {
	options DirectoryReaderOptions
	path    string
	client  *http.Client
}

// NewDirectoryReader binds server-side reads to the configured organization and provisioning domain.
func NewDirectoryReader(o DirectoryReaderOptions) (*DirectoryReader, error) {
	if o.TransportProfile == "" {
		o.TransportProfile = "production"
	}
	if contract.ValidateEndpoint(o.Issuer, o.TransportProfile) != nil ||
		!providerID.MatchString(o.OrganizationID) || len(o.Token) > 4096 || !providerToken.MatchString(o.Token) {
		return nil, contract.ErrInvalidArgument
	}
	origin, _ := url.Parse(o.Issuer)
	if origin.Path != "" && origin.Path != "/" {
		return nil, contract.ErrInvalidArgument
	}
	return &DirectoryReader{o, "/scim/v2/" + o.OrganizationID + "/Users", &http.Client{
		Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

func (r *DirectoryReader) request(ctx context.Context, path string, result any) (bool, error) {
	if err := contract.CheckCancellation(ctx); err != nil {
		return false, err
	}
	found, err := providerCall(ctx, r.client, r.options.Issuer, r.options.Token, path, nil, result, false)
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		return false, cancelled
	}
	var throttled *contract.ProviderThrottled
	if errors.As(err, &throttled) {
		return false, contract.ErrUnavailable
	}
	return found, err
}

func (r *DirectoryReader) List(ctx context.Context, startIndex, count int64) (DirectoryPage, error) {
	empty := DirectoryPage{}
	if r == nil || r.client == nil || ctx == nil || startIndex < 1 ||
		count < 1 || count > 50 || startIndex > 9007199254740991-count {
		return empty, contract.ErrInvalidArgument
	}
	var page struct {
		Resources    []DirectoryUser
		StartIndex   int64
		TotalResults *int64
	}
	found, err := r.request(ctx, fmt.Sprintf("%s?startIndex=%d&count=%d", r.path, startIndex, count), &page)
	if err != nil {
		return empty, err
	}
	if !found || page.StartIndex != startIndex || page.TotalResults == nil || *page.TotalResults < 0 ||
		*page.TotalResults > 9007199254740991 || page.Resources == nil || int64(len(page.Resources)) > count {
		return empty, contract.ErrUnavailable
	}
	seen := make(map[string]bool, len(page.Resources))
	for _, user := range page.Resources {
		if !providerID.MatchString(user.ID) || seen[user.ID] || !utf8.ValidString(user.ExternalID) || len(user.ExternalID) > 4096 {
			return empty, contract.ErrUnavailable
		}
		seen[user.ID] = true
	}
	result := DirectoryPage{Users: page.Resources}
	if int64(len(page.Resources)) == count {
		result.NextStartIndex = startIndex + count
	}
	return result, nil
}

func (r *DirectoryReader) Read(ctx context.Context, subject string) (DirectoryIdentityState, error) {
	empty := DirectoryIdentityState{}
	if r == nil || r.client == nil || ctx == nil || !providerID.MatchString(subject) {
		return empty, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response struct {
		User *struct {
			UserID, State string
			Details       struct{ ResourceOwner string }
			Human         map[string]any
		}
	}
	found, err := r.request(ctx, "/v2/users/"+subject, &response)
	if err != nil {
		return empty, err
	}
	if !found {
		// A mis-scoped reader can also receive 404. Verify access to this directory first.
		if _, err := r.List(ctx, 1, 1); err != nil {
			return empty, err
		}
		var resource DirectoryUser
		found, err := r.request(ctx, r.path+"/"+subject, &resource)
		if err != nil {
			return empty, err
		}
		if found {
			return empty, contract.ErrUnavailable
		}
		return DirectoryIdentityState{State: "missing"}, nil
	}
	user := response.User
	if user == nil {
		return empty, contract.ErrUnavailable
	}
	if user.UserID != subject || user.Details.ResourceOwner != r.options.OrganizationID || user.Human == nil {
		return empty, contract.ErrConflict
	}
	switch user.State {
	case "USER_STATE_DELETED":
		return DirectoryIdentityState{State: "missing"}, nil
	case "USER_STATE_INACTIVE", "USER_STATE_LOCKED":
		return DirectoryIdentityState{State: "inactive"}, nil
	case "USER_STATE_INITIAL":
		return DirectoryIdentityState{State: "initial"}, nil
	case "USER_STATE_ACTIVE":
		var resource struct {
			DirectoryUser
			Active bool
		}
		found, err := r.request(ctx, r.path+"/"+subject, &resource)
		if err != nil {
			return empty, err
		}
		if !found || !resource.Active || !providerID.MatchString(resource.ID) ||
			!utf8.ValidString(resource.ExternalID) || len(resource.ExternalID) > 4096 {
			return empty, contract.ErrUnavailable
		}
		if resource.ID != subject {
			return empty, contract.ErrConflict
		}
		return DirectoryIdentityState{State: "active", ExternalID: resource.ExternalID}, nil
	default:
		return empty, contract.ErrUnavailable
	}
}
