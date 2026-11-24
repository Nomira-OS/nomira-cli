package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeBlob(t *testing.T, dir, content string) string {
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
	layer := writeBlob(t, dir, "layer-bytes")
	config := writeBlob(t, dir, `{"architecture":"amd64"}`)
	stale := writeBlob(t, dir, "old-layer")
	staleManifest := writeBlob(t, dir, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+stale+`"}]}`)
	manifest := writeBlob(t, dir, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"`+config+`"},"layers":[{"digest":"`+layer+`"}]}`)
	// A nested index, as for multi-platform images.
	nested := writeBlob(t, dir, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"`+manifest+`"}]}`)
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
