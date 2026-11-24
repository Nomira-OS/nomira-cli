package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestAppendUKILayer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := testBlob(t, dir, "base-layer")
	config := testBlob(t, dir, `{"architecture":"amd64","rootfs":{"type":"layers","diff_ids":["sha256:aaaa"]},"history":[{"created_by":"base"}]}`)
	manifest := testBlob(t, dir, `{"schemaVersion":2,"mediaType":"`+mediaManifest+`","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"`+config+`","size":123},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+zstd","digest":"`+base+`","size":9007199254740993}]}`)
	index := `{"schemaVersion":2,"manifests":[{"mediaType":"` + mediaManifest + `","digest":"` + manifest + `","size":1,"annotations":{"org.opencontainers.image.ref.name":"latest"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the script in the image leaves behind.
	out := t.TempDir()
	for f, content := range map[string]string{
		"kernel/7.3.5-arch1-1/vmlinuz":       "kernel",
		"kernel/7.3.5-arch1-1/initramfs.img": "initrd",
		"boot/EFI/Linux/7.3.5-arch1-1.efi":   "MZ-uki",
	} {
		p := filepath.Join(out, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	layer, err := ukiLayer(out)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := ukiLayer(out)
	if !bytes.Equal(layer, again) {
		t.Error("layer differs between two runs")
	}
	var names []string
	tr := tar.NewReader(bytes.NewReader(layer))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "boot/EFI/Linux/7.3.5-arch1-1.efi" {
			if b, _ := io.ReadAll(tr); string(b) != "MZ-uki" {
				t.Errorf("UKI content %q", b)
			}
		}
	}
	sort.Strings(names)
	want := "boot/ boot/EFI/ boot/EFI/Linux/ boot/EFI/Linux/7.3.5-arch1-1.efi usr/lib/modules/7.3.5-arch1-1/.wh.initramfs.img usr/lib/modules/7.3.5-arch1-1/.wh.vmlinuz"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("layer entries:\n got %s\nwant %s", got, want)
	}

	if err := appendLayer(dir, layer, "nomira: unified kernel image"); err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Manifests []struct {
			Digest      string
			Size        int
			Annotations map[string]string
		}
	}
	if err := readJSON(filepath.Join(dir, "index.json"), &idx); err != nil {
		t.Fatal(err)
	}
	m := idx.Manifests[0]
	if m.Annotations["org.opencontainers.image.ref.name"] != "latest" || m.Digest == manifest {
		t.Fatalf("index: %+v", idx)
	}
	raw, _ := os.ReadFile(blobPath(dir, m.Digest))
	if len(raw) != m.Size {
		t.Errorf("manifest size %d, descriptor says %d", len(raw), m.Size)
	}
	if !strings.Contains(string(raw), `"size":9007199254740993`) {
		t.Errorf("layer size not kept exactly: %s", raw)
	}
	var man struct {
		Config struct{ Digest string }
		Layers []struct{ MediaType, Digest string }
	}
	json.Unmarshal(raw, &man)
	if len(man.Layers) != 2 || man.Layers[1].MediaType != mediaLayerTar || man.Layers[0].Digest != base {
		t.Fatalf("layers: %+v", man.Layers)
	}
	var cfg struct {
		Rootfs struct {
			DiffIDs []string `json:"diff_ids"`
		}
		History []struct {
			CreatedBy string `json:"created_by"`
		}
	}
	readJSON(blobPath(dir, man.Config.Digest), &cfg)
	if len(cfg.Rootfs.DiffIDs) != 2 || cfg.Rootfs.DiffIDs[1] != man.Layers[1].Digest || len(cfg.History) != 2 {
		t.Errorf("config: %+v", cfg)
	}

	// Pruning keeps the new image and drops the old manifest and config.
	if err := pruneLayout(dir); err != nil {
		t.Fatal(err)
	}
	for d, present := range map[string]bool{base: true, man.Layers[1].Digest: true, m.Digest: true, manifest: false, config: false} {
		if _, err := os.Stat(blobPath(dir, d)); (err == nil) != present {
			t.Errorf("%s: present=%v, want %v", d, err == nil, present)
		}
	}
}

func TestBuildAddsUKI(t *testing.T) {
	recipe := writeRecipe(t, "settings: {}\n")
	asUser(t, true)
	out := dryRun(t, "build", "--dry-run", "-f", recipe)
	steps := []string{"buildctl ", "+ skopeo copy oci:", "+ podman run --rm --pull=never", "type=image,source=localhost/mysystem:latest,destination=/target,rw=true", "/etc/systemd/ukify.conf:ro", "bootc container ukify", "+ nomira-append-layer"}
	pos := 0
	for _, s := range steps {
		i := strings.Index(out[pos:], s)
		if i < 0 {
			t.Fatalf("step %q missing or out of order in:\n%s", s, out)
		}
		pos += i + len(s)
	}
	if out := dryRun(t, "build", "--dry-run", "--no-uki", "-f", recipe); strings.Contains(out, "podman run --rm") || strings.Contains(out, "skopeo") {
		t.Errorf("--no-uki still builds a UKI:\n%s", out)
	}
}
