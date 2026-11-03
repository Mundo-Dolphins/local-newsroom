package memory

import (
	"fmt"
)

// AsVerifiedEvidence refuses, by construction, to interpret any memory
// record as verified factual evidence.
//
// A consumer that needs evidence must run the claim through the normal
// sourcing/verification pipeline (internal/researcher → internal/contracts
// VerificationResult). This guard exists so that a future convenience
// helper cannot quietly turn contextual memory into dossier evidence.
//
// It always returns ErrMemoryNotEvidence.
func AsVerifiedEvidence(m *MemoryRecord) error {
	if m == nil {
		return fmt.Errorf("%w: no record to consider", ErrMemoryNotEvidence)
	}
	return ErrMemoryNotEvidence
}
