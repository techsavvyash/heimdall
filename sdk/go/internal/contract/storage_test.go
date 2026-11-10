package contract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStorageCompatibility(t *testing.T) {
	var cases []struct {
		Name, Error string
		Metadata    json.RawMessage
		Policy      StoragePolicy
	}
	readAuditFixture(t, "storage-compatibility", &cases)
	schema := payloadSchema(t, "storage-metadata")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Metadata)
			var m StorageMetadata
			if err := json.Unmarshal(c.Metadata, &m); err != nil {
				t.Fatal(err)
			}
			err := ValidateStorageCompatibility(m, c.Policy)
			if c.Error == "" && err != nil {
				t.Fatal(err)
			}
			if c.Error != "" && !errors.Is(err, NewError(Code(c.Error), "")) {
				t.Fatalf("expected %s, got %v", c.Error, err)
			}
		})
	}
}
