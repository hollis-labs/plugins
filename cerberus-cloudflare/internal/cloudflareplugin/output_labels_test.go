package cloudflareplugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Every operation says what its output is (P4-3): an output_schema, derived
// from the DTO it returns, whose labels are valid. An operation without one
// would reach agents marked untrusted as a whole, and show as a gap in the
// install review.
func TestEveryOperationLabelsItsOutput(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.OutputSchema == nil {
			t.Errorf("operation %q has no output_schema", op.Name)
			continue
		}
		if _, err := contract.OutputLabelPointers(op.OutputSchema); err != nil {
			t.Errorf("operation %q: %v", op.Name, err)
		}
	}
}
