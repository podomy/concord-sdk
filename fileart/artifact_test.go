// Copyright (C) 2026 Podomy.
// SPDX-License-Identifier: Apache-2.0

package fileart

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry is a minimal OCI distribution server: blob existence,
// monolithic upload, manifest put, blob get. It counts blob PUTs so
// tests prove the smart-push skip.
type fakeRegistry struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	puts    int
	uploads int
}

func newFakeRegistry() (*httptest.Server, *fakeRegistry) {
	fake := &fakeRegistry{blobs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/concord-files/blobs/", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		digest := strings.TrimPrefix(r.URL.Path, "/v2/concord-files/blobs/")
		switch r.Method {
		case http.MethodHead:
			if _, ok := fake.blobs[digest]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodGet:
			data, ok := fake.blobs[digest]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data) //nolint:errcheck // test stub
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v2/concord-files/blobs/uploads/", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		fake.uploads++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fake.blobs[r.URL.Query().Get("digest")] = data
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/v2/concord-files/manifests/", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		fake.puts++
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	return server, fake
}

func dialFake(t *testing.T) (*Client, *fakeRegistry) {
	t.Helper()

	server, fake := newFakeRegistry()
	t.Cleanup(server.Close)
	client := NewClient(strings.TrimPrefix(server.URL, "http://"))
	client.http = server.Client()
	return client, fake
}

// Push round-trips bytes through the registry and returns a digest
// that streams back identical content.
func TestPushPullRoundTrip(t *testing.T) {
	t.Parallel()

	client, _ := dialFake(t)
	ctx := t.Context()

	digest, err := client.Push(ctx, []byte("calibration: 1.5"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q", digest)
	}

	dest := filepath.Join(t.TempDir(), "out")
	if err := client.PullTo(ctx, digest, dest); err != nil {
		t.Fatalf("pull: %v", err)
	}
	got, err := os.ReadFile(dest) //nolint:gosec // test reads a file it just pulled
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "calibration: 1.5" {
		t.Fatalf("pull = %q", got)
	}
}

// Re-pushing identical bytes uploads nothing: two HEADs, no PUT body.
// Dedup falls out of content addressing.
func TestPushSkipsExisting(t *testing.T) {
	t.Parallel()

	client, fake := dialFake(t)
	ctx := t.Context()

	if _, err := client.Push(ctx, []byte("shared")); err != nil {
		t.Fatalf("first push: %v", err)
	}
	uploads := fake.uploads
	if _, err := client.Push(ctx, []byte("shared")); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if fake.uploads != uploads {
		t.Fatalf("uploads = %d, want %d: shared content re-uploaded", fake.uploads, uploads)
	}
}

// Bytes that do not hash to the requested digest fail the pull, leave
// no file behind, and never reach a container.
func TestPullRejectsTampered(t *testing.T) {
	t.Parallel()

	client, fake := dialFake(t)
	ctx := t.Context()

	digest, err := client.Push(ctx, []byte("genuine"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	fake.mu.Lock()
	fake.blobs[digest] = []byte("forged")
	fake.mu.Unlock()

	dest := filepath.Join(t.TempDir(), "out")
	if err := client.PullTo(ctx, digest, dest); err == nil {
		t.Fatal("expected digest mismatch error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("tampered pull left %s behind", dest)
	}
}

// Materialize writes files with modes and parents. Bad paths and modes
// fail before any fetch, so a bad third file never pays for pulling
// the first two.
func TestMaterialize(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	root := t.TempDir()
	fetch := func(_ context.Context, digest, dest string) error {
		return os.WriteFile(dest, []byte("data:"+digest), 0o600)
	}

	files := []File{
		{Path: "/etc/app/config.yaml", Digest: "sha256:abc"},
		{Path: "/entrypoint.sh", Digest: "sha256:def", Mode: 0o755},
	}
	if err := Materialize(ctx, root, files, fetch); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	for _, tc := range []struct {
		path string
		mode string
	}{
		{"/etc/app/config.yaml", "600"},
		{"/entrypoint.sh", "755"},
	} {
		if got := fileMode(t, root+tc.path); got != tc.mode {
			t.Errorf("%s mode = %s, want %s", tc.path, got, tc.mode)
		}
	}
}

// fileMode returns the permission bits of a path as an octal string.
func fileMode(t *testing.T, path string) string {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%o", uint32(info.Mode().Perm()))
}

// Relative paths, unclean paths, escapes, and wild modes all fail
// before anything is written.
func TestMaterializeRejectsBadPaths(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	pristine := func(_ context.Context, _, _ string) error {
		t.Error("fetch must not run after validation failure")
		return nil
	}
	for _, path := range []string{"relative.yaml", "/etc/../escape", "/etc/./dots", "/ok"} {
		files := []File{{Path: path, Digest: "sha256:abc"}}
		if path == "/ok" {
			write := func(_ context.Context, _, dest string) error {
				return os.WriteFile(dest, []byte("x"), 0o600)
			}
			if err := Materialize(ctx, t.TempDir(), files, write); err != nil {
				t.Errorf("Materialize(%q) = %v", path, err)
			}
			continue
		}
		if err := Materialize(ctx, t.TempDir(), files, pristine); err == nil {
			t.Errorf("Materialize(%q) = nil, want error", path)
		}
	}
	if err := Materialize(ctx, t.TempDir(), []File{{Path: "/ok", Digest: "sha256:abc", Mode: 0o7777}}, pristine); err == nil {
		t.Error("expected error for mode above 0777")
	}
}

// A 5MB blob round-trips byte-identical through the fake registry.
// The pull path streams through a temp file with flat memory, never
// buffering the whole blob, and the digest names the exact bytes.
func TestPushPullLargeBlob(t *testing.T) {
	t.Parallel()

	client, _ := dialFake(t)
	ctx := t.Context()

	const size = 5 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i * 31) % 251)
	}

	digest, err := client.Push(ctx, data)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if digest != digestOf(data) {
		t.Fatalf("digest = %q, want content hash", digest)
	}

	dest := filepath.Join(t.TempDir(), "out")
	if err := client.PullTo(ctx, digest, dest); err != nil {
		t.Fatalf("pull: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("size = %d, want %d", info.Size(), size)
	}
	got, err := os.ReadFile(dest) //nolint:gosec // test reads a file it just pulled
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("pulled bytes differ from pushed bytes")
	}
}

// A cancelled context aborts the pull and leaves no file behind,
// so the next tick retries idempotently instead of resuming garbage.
func TestPullToRespectsCancel(t *testing.T) {
	t.Parallel()

	client, _ := dialFake(t)
	ctx := t.Context()

	digest, err := client.Push(ctx, []byte("cancel me"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	cancelled, stop := context.WithCancel(ctx)
	stop()

	dest := filepath.Join(t.TempDir(), "out")
	if err := client.PullTo(cancelled, digest, dest); err == nil {
		t.Fatal("expected context error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled pull left %s behind", dest)
	}
}
