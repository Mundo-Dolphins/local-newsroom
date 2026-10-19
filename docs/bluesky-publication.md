# Bluesky Publication Package (v0.6)

`internal/bluesky` is the Bluesky publication adapter. It publishes a set of
approved posts (for example a
[`ThreadRenderer`](../internal/renderers/renderer.go) output for the
`bluesky` editorial destination) to a Bluesky account as a **thread** over
the official AT Protocol xrpc surface, and returns a `PublicationReceipt`
naming the first post's AT URI with the per-post AT URIs + CIDs in its
metadata — resumable, redaction-safe, dry-run capable.

It sits on the generic social-publisher types in
[`internal/social`](../internal/social/social.go): the `Publisher` interface
(`Publish(ctx, Request) (Result, error)` + `Receipt(...)`), the `Post` /
`Facet` / `RecordRef` types, and the receipt-metadata encode/decode helpers
shared by every social adapter.

It closes [Mundo-Dolphins/local-newsroom#118](https://github.com/Mundo-Dolphins/local-newsroom/issues/118).

## What It Is / What It Is Not

| Is | Is not |
|---|---|
| A concrete social *publisher*: approved posts → one `com.atproto.repo.createRecord` call per post (collection `app.bsky.feed.post`) against a PDS, receipt with AT URIs, digest-bound | A client SDK: it implements exactly the three xrpc methods the pipeline needs (`createSession`, `refreshSession`, `createRecord`) against one endpoint |
| Thread-aware: the first post is a root; every later post carries a `reply` block whose `root` is post 1 and whose `parent` is the immediately preceding post — strong refs (AT URI + CID) captured from each server response, so the chain survives a partial failure | A reply composer: v0.6 refuses posts that carry an *external* reply reference (`external_reply` issue); the thread is the unit of publication |
| Credential-safe: the app password is read from an environment variable named by config (never stored in the struct, receipt, or logs) and exchanged for in-memory JWTs that rotate on expiry/401 | A credential vault: tokens live in process memory for the publisher's lifetime and are gone at exit; they never touch disk, receipts, or error strings |
| Validation-first: grapheme and byte limits, language tags, and facets are checked locally before any network I/O (dry run: no network at all) | A content transformer: it sends exactly the approved bytes — never truncating, rewrapping, or rewriting text (truncation would break the digest binding) |

## The Publication

```
approved posts (1..30, ≤ 300 graphemes / ≤ 3000 bytes each)
   │  validate (local: graphemes, bytes, langs, facets, thread length)
   ▼
dry run? ── yes ──► Result{DryRun: true, Issues, per-post counts} — zero network
   │ no
   ▼
login (com.atproto.server.createSession, password from env var)
   │
   ├─ resume? prior receipt's post_N refs fill the already-published prefix
   ▼
for each remaining post i:
   build record {$type, text, createdAt, langs, facets, reply{root, parent}}
   POST com.atproto.repo.createRecord
   capture (uri, cid)  ── 401? rotate session, retry once
   ▼
Result{Published: [uri|cid …], ExternalURI: canonical URL of post 1, Digest}
   ▼
Receipt (external record ID = post 1's AT URI; post_1..post_N + did_sha in
metadata; content digest = the approved content's digest)
```

One `Publish` call for a three-post thread makes one login and three
`createRecord` calls, in order. Post 2's `reply.parent` is post 1's
**server-returned** uri+cid; post 3's is post 2's — the chain is always
built from what the PDS confirmed, never from predicted values.

## Configuration Surface

`bluesky.Config` (validated by `New`; no credential is accepted in the
struct):

| Field | Default | Meaning |
|---|---|---|
| `Handle` | required | The account handle or `did:` (leading `@` tolerated; validated as a handle/URI) |
| `APIEndpoint` | `https://bsky.social` | The PDS base URL — absolute http(s), no embedded credentials, no trailing slash |
| `APIKeyEnv` | required | The **name** of an environment variable that holds the app password. `New` fails if the variable is unset or empty. The password itself is read once at construction, held in memory only, and never returned by any accessor |
| `DefaultLangs` | `{}` | Language tags applied to posts that carry none (≤ 3, conservative tag shape) |
| `Client` | bounded `*http.Client` (60 s timeout, redirects refused) | Test seam: an `*http.Client` whose transport points at a test server |

## Request and Result

`social.Request`: `PlanID` and `TargetID` (validated plan-contract
identifiers), `Posts` (1–30), and `PriorReceipt` (optional, for resume).
Each `social.Post` is the approved text (`langs`, `createdAt`, optional
`facets`); a post carrying an external `reply` reference is refused in
v0.6.

`social.Result`: `Published` (the server-returned `RecordRef{URI, CID}` per
post), `ExternalURI` (the canonical `https://bsky.app/profile/<handle>/post/<rkey>`
of post 1), `ExternalRecordID` (post 1's AT URI), `Digest` (NUL-joined
post texts — identical for dry and real runs, so a rehearsal record and the
receipt bind the same approved content), `DryRun`, `Issues`, `Details`
(per-post grapheme/byte counts in a dry run), and `Err` (the failure that
stopped a real run).

```go
pub, err := bluesky.New(cfg)
res, err := pub.Publish(ctx, social.Request{
    PlanID: planID, TargetID: "bluesky-thread",
    Posts:  posts,            // or: DryRun: true
    // PriorReceipt: rec,     // resume after a partial failure
})
rec, err := pub.Receipt(req, res, publishedAt) // dry-run results refuse
dr   := res.ToDryRunResult(at)                 // plan-contract rehearsal record
```

## Safety Model

Fail closed, at every boundary:

- **Credentials.** The app password exists only as `os.Getenv(APIKeyEnv)` in
  the session provider. The access and refresh JWTs exist only in the
  provider's memory. Every error surface runs through `run.RedactSecrets`,
  and `NewReceipt` re-redacts; a receipt whose metadata contains a
  credential-shaped value cannot be built. The password is never echoed in
  any error message (a login failure that repeats it is redacted before
  return).
- **Validation before wire.** Grapheme count (`rivo/uniseg` — a flag, a
  family emoji, or a combining sequence counts once) against the lexicon's
  300, raw bytes against 3000, up to 3 conservative `langs` tags, no control
  characters, at most 30 posts. Any issue fails the call **before the login**
  (a dry run instead reports it in `Issues` without any network I/O).
- **Facets.** Link facets are generated deterministically from the post text:
  every `http(s)://` URL is extended over URL-safe ASCII, balanced for
  parentheses, trimmed of trailing prose punctuation, and validated with
  `net/url`; the facet's `index` addresses **UTF-8 byte offsets**, so
  `text[byteStart:byteEnd]` is exactly the link. Caller-supplied link facets
  are validated (bounds, overlap, host, absolute http(s) URI) and suppress
  the duplicate auto-link for their span. Mention/tag facets are rejected
  with a clear `deferred_facet` issue in v0.6.
- **Sessions.** One login per `Publish`. An access JWT whose `exp` claim has
  passed is rotated with the refresh JWT before use; a 401 on any write
  triggers one rotation and one retry, and a second 401 fails the run
  (`ErrSession`).
- **Account binding.** A resumed thread must belong to the authenticated
  account: the receipt carries a 12-hex SHA-256 fingerprint of the account
  DID (`did_sha`) and a mismatch fails with `ErrAccountMismatch` before any
  write — posting another account's thread tail would silently break the
  chain.
- **Receipts.** `ContentDigest` is the digest of the **full approved** post
  set (never just the published prefix), so `PartialTarget` and `SucceedTarget`
  bind identically. `Metadata` stays inside the contract bounds (≤ 32 pairs;
  with the 30-post maximum the state is `post_1..post_N` + `did_sha` = N+1 ≤
  31 pairs; values are AT URIs/CIDs, which are not secret-shaped).
  **did:key accounts:** a `did:key` identifier is a long base64url blob that
  trips the contract's secret heuristics and would be redacted from the
  receipt, corrupting resume state — `Receipt` therefore returns a clear
  error for them (documented v0.6 limitation; `did:plc`/`did:web` are fully
  supported).

## Resume (Idempotency)

A `createRecord` failure mid-thread returns the partial `Result` (the posts
that landed, in order) together with the error. `pub.Receipt` on that result
produces a digest-bound receipt whose `post_N` metadata pairs are
`<at-uri>|<cid>` (the `|` separator cannot appear in either — AT URIs and
base32 CIDs are alphanumeric-plus-`._:/-`). A retried `Publish` with
`PriorReceipt` set:

1. decodes the published prefix and verifies the `did_sha` account binding;
2. logs in, and chains post N+1's `reply` onto the **last** published ref;
3. writes only the missing posts — an exact retry of a completed thread is a
   no-op (zero writes);
4. returns the full thread (prior + new refs) so the final receipt covers it.

Corrupt resume state (gaps in the `post_N` sequence, unparseable refs)
fails with `ErrResume` before any write.

## Errors

`ErrSession` (login/rotation), `ErrPublish` (a write failed; wraps the
redacted xrpc error), `ErrResume` (corrupt prior state),
`ErrAccountMismatch`, and `social.ErrDryRunReceipt` (a dry-run `Result`
cannot become a receipt). All match via `errors.Is`.

## Tests

`internal/bluesky` is covered by an httptest-based suite against a
`fakeXRPC` server that speaks the three xrpc endpoints (scripted statuses,
captured bodies and `Authorization` headers) with fixed times and
deterministic fake records (plausible `did:plc`, base32 CIDs, base64 rkeys):
config and credential-safety validation, login + bearer use, proactive JWT
rotation, 401 → rotate → retry, single-post wire format, thread chaining
(root/parent refs from real responses), grapheme/byte/lang/reply/facet
validation, byte-offset facets with multi-byte text, first- and mid-thread
failures, resume (one write for one missing post, exact-retry no-op,
corrupt-state refusal, account mismatch), auth redaction (password and JWT
echoed by a hostile server), dry-run zero-network with blocker reporting,
malformed responses, the did:key receipt rejection, 30-post metadata bounds,
and record-build determinism. Package coverage is tracked against the repo's
80% floor via `make check` / `make coverage`.
