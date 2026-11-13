package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
)

var invitationToken = regexp.MustCompile(`^[a-f0-9]{64}$`)

func invitationHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func invitationHandler(auth *heimdall.Client, sessions heimdall.Sessions, business *sql.DB, environment, origin, creator, eligible string, workspaceID *regexp.Regexp) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		ctx, identity := r.Context(), actor.Identity()
		fields, err := jsonInput(w, r)
		if err != nil {
			return err
		}
		if workspace := r.PathValue("workspace"); workspace != "" {
			var subject string
			var roles []string
			if !workspaceID.MatchString(workspace) || len(fields) != 2 ||
				json.Unmarshal(fields["subject"], &subject) != nil || subject == "" ||
				json.Unmarshal(fields["roles"], &roles) != nil || roles == nil {
				return heimdall.ErrInvalidArgument
			}
			if subject != creator && subject != eligible {
				return heimdall.ErrForbidden
			}
			var random [32]byte
			rand.Read(random[:])
			token := hex.EncodeToString(random[:])
			hash := invitationHash(token)
			target := heimdall.Identity{Issuer: identity.Issuer, Subject: subject}
			err = auth.Tenant(workspace).Memberships().AuthorizeInvitation(ctx, actor, heimdall.MembershipRolesInput{
				MembershipInput: heimdall.MembershipInput{Target: target, IdempotencyKey: hash}, Roles: roles,
			})
			if err != nil {
				return err
			}
			var expires time.Time
			err = business.QueryRowContext(ctx, `INSERT INTO support.invitations(environment,token_hash,workspace_id,issuer,recipient,inviter,roles,expires_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,now()+interval '1 hour') RETURNING expires_at`,
				environment, hash, workspace, identity.Issuer, subject, identity.Subject, string(fields["roles"])).Scan(&expires)
			if err != nil {
				return err
			}
			writeJSON(w, 201, map[string]any{"token": token, "expiresAt": expires})
			return nil
		}
		var token string
		if len(fields) != 1 || json.Unmarshal(fields["token"], &token) != nil || !invitationToken.MatchString(token) {
			return heimdall.ErrInvalidArgument
		}
		hash := invitationHash(token)
		claim := []any{environment, hash, identity.Issuer, identity.Subject}
		var workspace, inviter string
		var rolesJSON []byte
		var operation sql.NullString
		err = business.QueryRowContext(ctx, `UPDATE support.invitations SET claimed_at=COALESCE(claimed_at,now())
		  WHERE environment=$1 AND token_hash=$2 AND issuer=$3 AND recipient=$4
		  AND (claimed_at IS NOT NULL OR expires_at>now())
		  RETURNING workspace_id,inviter,roles,operation_id`, claim...).Scan(&workspace, &inviter, &rolesJSON, &operation)
		if errors.Is(err, sql.ErrNoRows) {
			return heimdall.ErrNotFound
		}
		if err != nil {
			return err
		}
		if operation.Valid {
			writeJSON(w, 200, heimdall.MembershipStatus{WorkspaceID: workspace, OperationID: operation.String, Status: "confirmed"})
			return nil
		}
		var roles []string
		if json.Unmarshal(rolesJSON, &roles) != nil || roles == nil {
			return heimdall.ErrUnavailable
		}
		result, err := auth.Tenant(workspace).Memberships().AcceptInvitation(ctx, actor,
			heimdall.Identity{Issuer: identity.Issuer, Subject: inviter}, heimdall.MembershipRolesInput{
				MembershipInput: heimdall.MembershipInput{Target: identity, IdempotencyKey: hash}, Roles: roles,
			})
		if err != nil {
			return err
		}
		status := 202
		if result.Status == "confirmed" {
			status = 200
			_, err = business.ExecContext(ctx, `UPDATE support.invitations SET operation_id=$5
			  WHERE environment=$1 AND token_hash=$2 AND issuer=$3 AND recipient=$4`, append(claim, result.OperationID)...)
			if err != nil {
				return err
			}
		}
		writeJSON(w, status, result)
		return nil
	}
}
