package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBlob(t *testing.T, dir, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	h := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "blobs", "sha256", h), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + h
}

func TestPruneLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	layer := testBlob(t, dir, "layer-bytes")
	config := testBlob(t, dir, `{"architecture":"amd64"}`)
	stale := testBlob(t, dir, "old-layer")
	staleManifest := testBlob(t, dir, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+stale+`"}]}`)
	manifest := testBlob(t, dir, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+layer+`"}]}`)
	// A nested index, as for multi-platform images.
	nested := testBlob(t, dir, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"`+manifest+`"}]}`)
	index := `{"schemaVersion":2,"manifests":[{"digest":"` + nested + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := pruneLayout(dir); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{layer: true, config: true, manifest: true, nested: true, stale: false, staleManifest: false} {
		_, err := os.Stat(blobPath(dir, d))
		if got := err == nil; got != want {
			t.Errorf("%s: present=%v, want %v", d, got, want)
		}
	}
}

func TestPublish(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sys.oci")
	if err := os.MkdirAll(filepath.Join(work, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	layer := testBlob(t, work, "layer")
	config := testBlob(t, work, `{"architecture":"amd64"}`)
	manifest := testBlob(t, work, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+layer+`"}]}`)
	unused := testBlob(t, work, "left over in the work directory")
	os.WriteFile(filepath.Join(work, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644)
	os.WriteFile(filepath.Join(work, "index.json"), []byte(`{"schemaVersion":2,"manifests":[{"digest":"`+manifest+`"}]}`), 0o644)

	if err := publish(work, dir); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{layer: true, config: true, manifest: true, unused: false} {
		if _, err := os.Stat(blobPath(dir, d)); (err == nil) != want {
			t.Errorf("%s: present=%v, want %v", d, err == nil, want)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if !strings.Contains(string(got), manifest) {
		t.Errorf("index.json: %s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.new")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// A second image replaces the first; the old blobs go.
	layer2 := testBlob(t, work, "layer 2")
	manifest2 := testBlob(t, work, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+layer2+`"}]}`)
	os.WriteFile(filepath.Join(work, "index.json"), []byte(`{"schemaVersion":2,"manifests":[{"digest":"`+manifest2+`"}]}`), 0o644)
	if err := publish(work, dir); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]bool{layer2: true, manifest2: true, config: true, layer: false, manifest: false} {
		if _, err := os.Stat(blobPath(dir, d)); (err == nil) != want {
			t.Errorf("second publish: %s: present=%v, want %v", d, err == nil, want)
		}
	}
}
