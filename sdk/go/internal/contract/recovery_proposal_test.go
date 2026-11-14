package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestRecoveryProposal(t *testing.T) {
	var f struct {
		Digest  string
		Request map[string]any
		Options struct {
			TerminalID, RequestID, SDKVersion string
			Actor                             map[string]any
			At, Cutoff                        int64
		}
		Proposal       RecoveryProposal
		InvalidOptions []struct {
			Name    string
			Options RecoveryOptions
		}
		InvalidProposals []struct {
			Name     string
			Proposal json.RawMessage
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/recovery-proposal.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	options := RecoveryOptions{f.Options.TerminalID, f.Options.RequestID, f.Options.SDKVersion, f.Options.Actor, f.Options.At, f.Options.Cutoff}
	p, e := BuildRecoveryProposal(f.Request, options)
	if e != nil || !reflect.DeepEqual(p, &f.Proposal) {
		t.Fatal(p, e)
	}
	digest, e := RecoveryProposalDigest(f.Request, p)
	if e != nil || digest != f.Digest {
		t.Fatal(digest, e)
	}
	otherOptions := options
	otherOptions.Actor = map[string]any{"kind": "system", "id": "another-initiator"}
	other, e := BuildRecoveryProposal(f.Request, otherOptions)
	if e != nil {
		t.Fatal(e)
	}
	otherDigest, e := RecoveryProposalDigest(f.Request, other)
	if e != nil || otherDigest == digest {
		t.Fatal(otherDigest, e)
	}
	encoded, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := DecodeRecoveryProposal(f.Request, encoded)
	if e != nil || !reflect.DeepEqual(decoded, p) {
		t.Fatal(decoded, e)
	}
	if e = ValidateRecoveryProposal(f.Request, p); e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeRecoveryProposal(f.Request, append(encoded, []byte(" null")...)); e == nil {
		t.Fatal("trailing JSON")
	}
	for _, c := range f.InvalidOptions {
		t.Run(c.Name, func(t *testing.T) {
			if _, e := BuildRecoveryProposal(f.Request, c.Options); e == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	for _, c := range f.InvalidProposals {
		t.Run(c.Name, func(t *testing.T) {
			if _, e := DecodeRecoveryProposal(f.Request, c.Proposal); e == nil {
				t.Fatal("invalid proposal accepted")
			}
		})
	}
	f.Request["requestId"] = "changed"
	if e = ValidateRecoveryProposal(f.Request, p); e == nil {
		t.Fatal("changed history accepted")
	}
	f.Request["actor"].(map[string]any)["subject"] = "changed"
	options.Actor["id"] = "changed"
	if !reflect.DeepEqual(p, &f.Proposal) {
		t.Fatal("proposal aliases caller data")
	}
}
