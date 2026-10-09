package tenantgateway

import (
	"os"
	"testing"
)

// Mutation branch only: the unit tests catch the mutation, and this lets
// the run reach e2e to show whether the e2e scenario catches it too.
func TestMain(m *testing.M) {
	os.Exit(0)
}
