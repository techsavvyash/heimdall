package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
)

func directoryMapping() ([]string, error) {
	var workspaces []string
	if json.Unmarshal([]byte(os.Getenv("HEIMDALL_DIRECTORY_WORKSPACES")), &workspaces) != nil ||
		len(workspaces) == 0 || len(workspaces) > 100 {
		return nil, heimdall.ErrInvalidArgument
	}
	validID := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	seen := make(map[string]bool)
	for _, id := range workspaces {
		if !validID.MatchString(id) || seen[id] {
			return nil, heimdall.ErrInvalidArgument
		}
		seen[id] = true
	}
	return workspaces, nil
}

func authorizeDirectoryCleanup(ctx context.Context, auth *heimdall.Client, actor *heimdall.Principal, workspaces []string, manifestJSON []byte) error {
	var manifest struct{ OwnerRole string }
	if json.Unmarshal(manifestJSON, &manifest) != nil {
		return heimdall.ErrInvalidArgument
	}
	for _, workspace := range workspaces {
		roles, err := auth.Tenant(workspace).Roles(ctx, actor)
		if err != nil {
			return err
		}
		if !slices.Contains(roles, manifest.OwnerRole) {
			return heimdall.ErrForbidden
		}
	}
	return nil
}

func directoryCleanupRoute(cleanup *postgres.DirectoryCleanup, auth *heimdall.Client, sessions heimdall.Sessions, origin, operator string, workspaces []string, manifestJSON []byte) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		if actor.Identity().Subject != operator {
			return heimdall.ErrForbidden
		}
		fields, err := jsonInput(w, r)
		if err != nil {
			return err
		}
		var subject, revision string
		if len(fields) != 2 || json.Unmarshal(fields["subject"], &subject) != nil ||
			json.Unmarshal(fields["revision"], &revision) != nil {
			return heimdall.ErrInvalidArgument
		}
		ctx := r.Context()
		if err := authorizeDirectoryCleanup(ctx, auth, actor, workspaces, manifestJSON); err != nil {
			return err
		}
		result, err := cleanup.Reconcile(ctx, auth, actor, subject, revision, workspaces)
		if err != nil {
			return err
		}
		if result == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
		} else {
			writeJSON(w, http.StatusAccepted, result)
		}
		return nil
	}
}
