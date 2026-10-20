package run_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// TestStatusVocabularyMatchesPublicationContract is the drift guard for the
// run package's mirrored publication status vocabulary: the manifest
// summarizes plan status with the run package's constants (the publication
// package already imports run, so it cannot import back), and the two
// vocabularies must never diverge.
func TestStatusVocabularyMatchesPublicationContract(t *testing.T) {
	want := map[string]bool{}
	for _, s := range publication.AllStatuses {
		want[string(s)] = true
	}
	got := map[string]bool{}
	for _, s := range run.AllPublicationStatuses {
		got[string(s)] = true
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("run's mirrored publication statuses diverged from the contract:\nrun: %v\ncontract: %v", got, want)
	}

	for _, s := range run.AllPublicationStatuses {
		if run.PublicationStatusTerminal(s) != publication.PublicationStatus(s).Terminal() {
			t.Fatalf("terminal semantics diverge for %q: run says %v, contract says %v",
				s, run.PublicationStatusTerminal(s), publication.PublicationStatus(s).Terminal())
		}
	}
}
