// The persistence layer of editorial change analysis (issue #147): it maps
// an analysis Result onto the v0.7 editorial-memory contracts
// (internal/memory) and stores it through a memorystore.Store.
//
// The mapping:
//
//   - each classified diff hunk becomes ONE record, kind
//     editorial_feedback, scope the affected recipe (or profile, or the
//     run), subject the affected document:
//       * the record's Field is the run-relative path of the final
//         document (the affected artifact);
//       * the Value is a FeedbackRecord whose Feedback text embeds the
//         BEFORE and AFTER excerpts of the hunk, the preserved
//         deterministic diff of the hunk, the (normalized) categories, and
//         the confidence — everything a later pattern-detection pass
//         (issue #148) needs, in one structured payload;
//       * the record's Confidence is the classifier's confidence mapped to
//         the memory confidence ladder (a classifier label is advisory:
//         medium at most, never "high" memory confidence — that ladder is
//         for verified knowledge, and a label from a model is not
//         verification);
//       * the record's Provenance carries the run, the generated/final
//         document paths and digests, and the editor (UserCorrected with
//         the editor identity).
//   - additionally, one record of kind correction per analysis when the
//     analysis reports a fact-changing classification at or above the
//     high-confidence threshold: the ticket requires that factual
//     corrections be distinguishable, and the correction kind is the
//     memory-contract home for "what the generated text got wrong and what
//     the final says instead."
//
// Persistence rules (inherited from memorystore, enforced here by
// construction):
//
//   - the original diff is preserved on every record (the Design section of
//     the ticket: "keep original diff");
//   - every field crosses the store's sanitization gate (fail closed on
//     secrets);
//   - writes are idempotent by content-derived stable ID: re-analyzing the
//     same unedited pair re-derives the same records and is a no-op;
//   - a store refusal (secret, validation) returns the error; the analysis
//     itself (the Result) is already in hand and is unaffected.

package feedback
