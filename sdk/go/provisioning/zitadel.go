package provisioning

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"net/http"
	"strings"
	"time"
)

func newDispatchID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", contract.ErrUnavailable
	}
	random[6] = random[6]&0x0f | 0x40
	random[8] = random[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:]), nil
}
func zitadelCall(ctx context.Context, client *http.Client, issuer, token, path string, payload any, result any) (bool, error) {
	return providerCall(ctx, client, issuer, token, path, payload, result, true)
}

func providerCall(ctx context.Context, client *http.Client, base, token, path string, payload any, result any, connect bool, organization ...string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, contract.ErrInvalidArgument
	}
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		method, body = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, body)
	if err != nil {
		return false, contract.ErrInvalidArgument
	}
	req.Header.Set("Content-Type", "application/json")
	if connect {
		req.Header.Set("Connect-Protocol-Version", "1")
	}
	if len(organization) == 1 {
		req.Header.Set("x-zitadel-orgid", organization[0])
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
			return false, cancelled
		}
		return false, contract.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return false, contract.Throttled(resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return false, contract.ErrUnavailable
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit || json.Unmarshal(data, result) != nil {
		return false, contract.ErrUnavailable
	}
	return true, nil
}
