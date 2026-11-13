package browserlogin_test

import (
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/browserlogin"
	"github.com/techsavvyash/heimdall/sdk/go/sessionstoretest"
)

func TestSessionStoreConformance(t *testing.T) {
	store := browserlogin.NewMemorySessionStoreForTest()
	sessionstoretest.Verify(t, store, store)
}
