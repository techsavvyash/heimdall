package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func runDirectoryJob(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	configJSON := []byte(os.Getenv("HEIMDALL_CONFIG"))
	var config struct{ ProductID, Environment, Issuer string }
	var workspaces []string
	if json.Unmarshal(configJSON, &config) != nil || config.ProductID != "support" ||
		json.Unmarshal([]byte(os.Getenv("HEIMDALL_DIRECTORY_WORKSPACES")), &workspaces) != nil ||
		len(workspaces) == 0 || len(workspaces) > 100 {
		return heimdall.ErrInvalidArgument
	}
	validID := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	seen := make(map[string]bool)
	for _, id := range workspaces {
		if !validID.MatchString(id) || seen[id] {
			return heimdall.ErrInvalidArgument
		}
		seen[id] = true
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return heimdall.ErrInvalidArgument
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	options := provisioning.DirectoryReaderOptions{
		Issuer: config.Issuer, OrganizationID: os.Getenv("HEIMDALL_DIRECTORY_ORGANIZATION"),
		Token: os.Getenv("HEIMDALL_DIRECTORY_READER_TOKEN"),
	}
	polling, err := postgres.NewDirectoryPolling(db, config.ProductID, config.Environment, options)
	if err != nil {
		return err
	}
	discovery, err := polling.Discover(ctx, 25)
	if err != nil {
		return err
	}
	var observed int
	var invalidated int64
	for after := ""; ; {
		page, err := polling.ObserveKnown(ctx, after, 10)
		if err != nil {
			return err
		}
		observed += page.Observed
		invalidated += page.SessionsInvalidated
		after = page.NextSubject
		if after == "" {
			break
		}
	}
	fmt.Printf("{\"discovered\":%d,\"cycleComplete\":%t,\"observed\":%d,\"sessionsInvalidated\":%d}\n",
		discovery.Discovered, discovery.CycleComplete, observed, invalidated)

	cleanup, err := postgres.NewDirectoryCleanup(db, config.ProductID, config.Environment, options.Issuer, options.OrganizationID, "production")
	if err != nil {
		return err
	}
	pending, err := cleanup.Pending(ctx, "", 10)
	if err != nil {
		return err
	}
	var oldestAge int64
	if len(pending) > 0 {
		tenantSSO, err := tenantSSOPolicy()
		if err != nil {
			return err
		}
		storage, err := postgres.NewManagedStorage(db, postgres.StorageOptions{
			ProductID: config.ProductID, Environment: config.Environment,
		})
		if err != nil {
			return err
		}
		credential := func(name string) func(context.Context) (string, error) {
			return func(context.Context) (string, error) {
				value := os.Getenv(name)
				if value == "" {
					return "", heimdall.ErrInvalidArgument
				}
				return value, nil
			}
		}
		runtime, err := heimdall.New(ctx, configJSON, []byte(os.Getenv("HEIMDALL_MANIFEST")), heimdall.Options{
			PolicyVersion: os.Getenv("HEIMDALL_POLICY_VERSION"), Storage: storage, TenantSSOPolicy: tenantSSO,
			Credentials: heimdall.Credentials{
				ClientSecret: credential("HEIMDALL_CLIENT_SECRET"),
				OpenFGAKey:   credential("HEIMDALL_OPENFGA_KEY"),
			},
		})
		if err != nil {
			return err
		}
		actor, err := runtime.Tokens().Verify(ctx, os.Getenv("HEIMDALL_DIRECTORY_OPERATOR_TOKEN"))
		if err != nil {
			return err
		}
		for _, item := range pending {
			result, err := cleanup.Reconcile(ctx, runtime, actor, item.Subject, item.Revision, workspaces)
			if err != nil {
				return err
			}
			if result != nil {
				return heimdall.ErrOutcomeUnknown
			}
		}
		oldestAge = max(0, time.Now().UnixMilli()-pending[0].ObservedAt)
	}
	fmt.Printf("{\"cleaned\":%d,\"oldestCleanupAgeMs\":%d}\n", len(pending), oldestAge)
	return nil
}

func directoryJobExitCode(ctx context.Context) int {
	if err := runDirectoryJob(ctx); err != nil {
		code := "failed"
		var failure *heimdall.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
		}
		fmt.Fprintf(os.Stderr, "{\"directoryJob\":%q}\n", code)
		return 1
	}
	return 0
}
