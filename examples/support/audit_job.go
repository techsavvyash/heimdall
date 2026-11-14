package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func runAuditJob(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var config struct{ ProductID, Environment, Issuer string }
	dsn := os.Getenv("DATABASE_URL")
	if json.Unmarshal([]byte(os.Getenv("HEIMDALL_CONFIG")), &config) != nil || config.ProductID != "support" || dsn == "" {
		return heimdall.ErrInvalidArgument
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	sink, tenant := os.Getenv("HEIMDALL_AUDIT_SINK"), os.Getenv("HEIMDALL_AUDIT_TENANT")
	if sink == "" {
		sink = "siem"
	}
	delivery, err := postgres.NewAuditDelivery(db, postgres.AuditDeliveryOptions{
		ProductID: config.ProductID, Environment: config.Environment, SinkID: sink, TenantID: tenant,
		Endpoint: os.Getenv("HEIMDALL_AUDIT_ENDPOINT"), Token: os.Getenv("HEIMDALL_AUDIT_TOKEN"),
		TransportProfile: os.Getenv("HEIMDALL_AUDIT_TRANSPORT_PROFILE"), ValidateCustom: validateTicketRead,
	})
	if err != nil {
		return err
	}
	var collectionErr error
	if mapping := os.Getenv("HEIMDALL_IDENTITY_MAPPING"); mapping != "" {
		collectionErr = collectIdentityAudit(ctx, db, mapping, config.ProductID, config.Environment, config.Issuer, sink, tenant)
	}
	result, err := delivery.Deliver(ctx, 20)
	if err != nil {
		return err
	}
	metrics, err := delivery.Metrics(ctx)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"attempted": result.Attempted, "delivered": result.Delivered, "quarantined": result.Quarantined,
		"metrics": metrics,
	}); err != nil {
		return err
	}
	return collectionErr
}

func collectIdentityAudit(ctx context.Context, db *sql.DB, raw, product, environment, issuer, sink, tenant string) error {
	var mapping struct {
		OrganizationID, StartMicros string
		Aggregates                  []postgres.IdentityAggregate
		Coverage                    postgres.IdentityCoveragePolicy
	}
	if json.Unmarshal([]byte(raw), &mapping) != nil || tenant == "" {
		return heimdall.ErrInvalidArgument
	}
	start, err := strconv.ParseInt(mapping.StartMicros, 10, 64)
	if err != nil || start <= 0 || strconv.FormatInt(start, 10) != mapping.StartMicros {
		return heimdall.ErrInvalidArgument
	}
	collector, err := postgres.NewIdentityCollector(db, postgres.IdentityCollectorOptions{
		IdentityEventReaderOptions: provisioning.IdentityEventReaderOptions{
			Issuer: issuer, OrganizationID: mapping.OrganizationID, Token: os.Getenv("HEIMDALL_IDENTITY_READER_TOKEN"),
		},
		ProductID: product, Environment: environment, TenantID: tenant,
		StartMicros: start, Aggregates: mapping.Aggregates, AuditSinks: []string{sink},
	})
	if err != nil {
		return err
	}
	coverage, err := postgres.DeriveIdentityEventCoverage(mapping.Coverage, time.Now())
	if err != nil {
		return err
	}
	result, err := collector.Collect(ctx, coverage)
	if err != nil {
		return err
	}
	fmt.Printf("{\"collected\":%d,\"nextMicros\":%q}\n", result.Recorded, strconv.FormatInt(result.NextMicros, 10))
	return nil
}

func auditJobExitCode(ctx context.Context) int {
	if err := runAuditJob(ctx); err != nil {
		code := "failed"
		var failure *heimdall.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
		}
		fmt.Fprintf(os.Stderr, "{\"auditJob\":%q}\n", code)
		return 1
	}
	return 0
}
