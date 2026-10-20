package publish

import (
	"bytes"
	"fmt"
	"strings"
)

// The canonical thread content form.
//
// A thread target's publication content is the post texts joined in publish
// order by a single NUL byte (0x00). Two properties make this the form a
// thread plan baselines to:
//
//  1. Digest binding. social.DigestOfPosts digests exactly the NUL-joined
//     text, so a plan baselined to these bytes and a receipt built from the
//     posts a Bluesky publisher sent always carry the same digest — the
//     state machine's "the publisher sent exactly what was approved" check
//     holds by construction.
//
//  2. Unambiguous recovery. Valid post texts never contain a NUL byte
//     (control characters are rejected by the adapter validation), so the
//     approved bytes split back into the exact posts with no guessing.
//
// The rendered output/thread.txt file uses a human-readable blank-line
// joining and is NOT the canonical form: a thread plan is baselined to
// content derived from it (or from the write stage's artifact posts),
// persisted with SaveContent, and that file is what the plan references.

// threadNUL separates posts in the canonical thread content form.
const threadNUL = "\x00"

// ThreadContent canonicalizes post texts into thread publication content.
//
// Empty posts are rejected (a thread of fewer than one post is not a
// thread; the adapter would reject them anyway, and an empty segment in the
// canonical form is a content defect, not a publishable post).
func ThreadContent(posts ...string) ([]byte, error) {
	if len(posts) == 0 {
		return nil, fmt.Errorf("a thread requires at least one post (got none)")
	}
	var buf bytes.Buffer
	for i, p := range posts {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("post %d of %d is empty", i+1, len(posts))
		}
		if strings.Contains(p, threadNUL) {
			return nil, fmt.Errorf("post %d of %d contains a NUL byte; post texts must not", i+1, len(posts))
		}
		if i > 0 {
			buf.WriteString(threadNUL)
		}
		buf.WriteString(p)
	}
	return buf.Bytes(), nil
}

// ThreadPosts recovers the post texts from canonical thread content (the
// inverse of ThreadContent). The split is on the NUL separator only: a post
// may contain any other bytes, including newlines and blank lines.
//
// An empty segment (two adjacent NULs, or leading/trailing NUL) is a
// content defect — the content was not produced by ThreadContent — and is
// rejected rather than published.
func ThreadPosts(content []byte) ([]string, error) {
	if len(content) == 0 {
		return nil, fmt.Errorf("thread content is empty")
	}
	segments := strings.Split(string(content), threadNUL)
	for i, s := range segments {
		if s == "" {
			return nil, fmt.Errorf("thread content has an empty segment at position %d (malformed: content must be ThreadContent output)", i)
		}
	}
	out := make([]string, len(segments))
	copy(out, segments)
	return out, nil
}
