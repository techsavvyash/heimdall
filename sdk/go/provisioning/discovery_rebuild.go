package provisioning

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type DiscoveryRebuildCheckpoint struct {
	ProductID       string `json:"productId"`
	Environment     string `json:"environment"`
	WorkspaceID     string `json:"workspaceId"`
	StoreID         string `json:"storeId"`
	ModelID         string `json:"modelId"`
	ManifestVersion int    `json:"manifestVersion"`
	Phase           string `json:"phase"`
	Position        string `json:"position"`
}

type DiscoveryRebuildOptions struct {
	WorkspaceID, PolicyVersion, OpenFGAToken string
	AuditSinks                               []string
	Checkpoint                               *DiscoveryRebuildCheckpoint
}

type DiscoveryRebuildResult struct {
	Processed  int                         `json:"processed"`
	Checkpoint *DiscoveryRebuildCheckpoint `json:"checkpoint"`
}

var candidatePosition = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)

// RebuildDiscovery runs one bounded operator page. Persist the checkpoint only after success.
func RebuildDiscovery(ctx context.Context, db *sql.DB, configJSON, manifestJSON []byte, options DiscoveryRebuildOptions) (DiscoveryRebuildResult, error) {
	empty := DiscoveryRebuildResult{}
	d, err := configuration.Parse(configJSON, manifestJSON)
	if err != nil {
		return empty, err
	}
	cfg := d.Config()
	id := options.WorkspaceID
	if ctx == nil || !providerID.MatchString(id) || !providerID.MatchString(options.PolicyVersion) ||
		len(options.OpenFGAToken) > 8192 || !providerToken.MatchString(options.OpenFGAToken) {
		return empty, contract.ErrInvalidArgument
	}
	next := DiscoveryRebuildCheckpoint{ProductID: cfg.ProductID, Environment: cfg.Environment, WorkspaceID: id,
		StoreID: cfg.OpenFGA.StoreID, ModelID: cfg.OpenFGA.ModelID, ManifestVersion: cfg.ManifestVersion, Phase: "remote"}
	if options.Checkpoint != nil {
		prior := *options.Checkpoint
		next.Phase, next.Position = prior.Phase, prior.Position
		if prior != next {
			return empty, contract.ErrInvalidArgument
		}
		switch prior.Phase {
		case "remote":
			if prior.Position == "" || len(prior.Position) > 8192 || !utf8.ValidString(prior.Position) {
				return empty, contract.ErrInvalidArgument
			}
		case "candidates":
			if _, err := strconv.ParseInt(prior.Position, 10, 64); err != nil || !candidatePosition.MatchString(prior.Position) {
				return empty, contract.ErrInvalidArgument
			}
		default:
			return empty, contract.ErrInvalidArgument
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	workspaces, err := postgres.NewRetainedWorkspaceRecords(db, cfg.ProductID, cfg.Environment)
	if err != nil {
		return empty, err
	}
	artifact := d.Artifact()
	records, err := postgres.NewRetainedOperationRecords(db, postgres.OperationBinding{
		ProductID: cfg.ProductID, Environment: cfg.Environment, StoreID: next.StoreID,
		ModelID: next.ModelID, ModelHash: artifact.ModelHash, ManifestVersion: cfg.ManifestVersion,
	}, options.AuditSinks)
	if err != nil {
		return empty, err
	}
	before, err := workspaces.Get(ctx, id)
	if err != nil {
		return empty, err
	}
	if before == nil {
		return empty, contract.ErrNotFound
	}
	if before.Lifecycle != "active" || before.MutationBlocked {
		return empty, contract.ErrConflict
	}
	profile := cfg.TransportProfile
	if profile == "" {
		profile = "production"
	}
	model, err := modelbinding.New(modelbinding.Config{URL: cfg.OpenFGA.URL, StoreID: next.StoreID, ModelID: next.ModelID, Profile: profile},
		d.Manifest(), contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: profile},
		artifact, func(context.Context) (string, error) { return options.OpenFGAToken, nil })
	if err != nil {
		return empty, err
	}
	if _, err = model.Verify(ctx); err != nil {
		return empty, err
	}
	at := max(time.Now().UnixMilli(), before.UpdatedAt)
	observations := make([]postgres.CandidateObservation, 0, 20)
	observe := func(user string, check bool) error {
		identity, err := contract.DecodeIdentity(user)
		if err != nil || identity.Issuer != cfg.Issuer {
			return contract.ErrUnavailable
		}
		member := true
		if check {
			member, err = model.CheckMembership(ctx, user, id)
			if err != nil {
				return err
			}
		}
		observations = append(observations, postgres.CandidateObservation{Identity: identity, Member: member, ObservedAt: at})
		return nil
	}
	result := DiscoveryRebuildResult{Checkpoint: &next}
	if next.Phase == "remote" {
		users, position, err := model.ReadDirectMembers(ctx, id, next.Position)
		if err != nil {
			return empty, err
		}
		for _, user := range users {
			if err := observe(user, false); err != nil {
				return empty, err
			}
		}
		next.Position = position
		if position == "" {
			next.Phase, next.Position = "candidates", "0"
		}
	} else {
		rows, more, err := records.CandidateRepairPage(ctx, id, next.Position)
		if err != nil {
			return empty, err
		}
		for _, row := range rows {
			if err := observe(row.Principal, true); err != nil {
				return empty, err
			}
		}
		if more {
			next.Position = rows[len(rows)-1].Position
		} else {
			result.Checkpoint = nil
		}
	}
	if len(observations) > 0 {
		eventID, err := newDispatchID()
		if err != nil {
			return empty, err
		}
		if _, err = records.RepairCandidates(ctx, id, before.Revision, observations, postgres.CandidateRepairMetadata{
			EventID: eventID, RequestID: eventID, PolicyVersion: options.PolicyVersion,
			OccurredAt: at, RecordedAt: max(time.Now().UnixMilli(), at),
		}); err != nil {
			return empty, err
		}
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	result.Processed = len(observations)
	return result, nil
}
