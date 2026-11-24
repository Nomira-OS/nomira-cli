package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"
)

// ukiScript runs inside the freshly built image, with that image's root
// file system mounted writable (a throwaway copy) at /target. It uses the
// image's own bootc, so the composefs digest is computed by the same
// version that checks it at boot:
//
//  1. move kernel and initramfs out of /target, since the UKI embeds them;
//  2. build the UKI: bootc computes the digest of /target, adds the kernel
//     arguments from /usr/lib/bootc/kargs.d and calls ukify.
//
// /out/kernel/<kver>/ then lists what was moved, /out/boot/ holds the UKI.
const ukiScript = `set -eu
set -- /target/usr/lib/modules/*/vmlinuz
if [ "$#" != 1 ] || [ ! -e "$1" ]; then
    echo "nomira: expected exactly one kernel in /usr/lib/modules, found: $*" >&2
    exit 1
fi
kver="$(basename "$(dirname "$1")")"
bootc container split-kernel-and-rootfs --rootfs /target --output /out/kernel
mkdir -p /out/boot/EFI/Linux
bootc container ukify --rootfs /target --kernel-dir "/out/kernel/$kver" -- \
    --output "/out/boot/EFI/Linux/$kver.efi"
`

// addUKI builds a unified kernel image for the image ref (in
// containers-storage) and appends it to the layout directory dir as one
// more layer. All other layers stay as they are, so only the UKI layer is
// new on every build. The layer also removes the kernel and initramfs from
// /usr/lib/modules, as the composefs digest in the UKI was computed without
// them.
func addUKI(r *runner, dir, ref string) error {
	work := "<tmp>"
	if !r.dryRun {
		var err error
		if work, err = os.MkdirTemp("", "nomira-uki-"); err != nil {
			return err
		}
		defer os.RemoveAll(work)
	}
	out := filepath.Join(work, "out")
	conf := filepath.Join(work, "ukify.conf")
	if !r.dryRun {
		if err := os.Mkdir(out, 0o755); err != nil {
			return err
		}
		// ukify reads /etc/systemd/ukify.conf from the image it runs in.
		// An empty file keeps a configuration shipped by a recipe from
		// deciding about signing.
		if err := os.WriteFile(conf, nil, 0o644); err != nil {
			return err
		}
	}
	err := r.run("podman", "run", "--rm", "--pull=never", "--network=none",
		"--security-opt", "label=disable",
		"--mount", "type=image,source="+ref+",destination=/target,rw=true",
		"-v", out+":/out",
		"-v", conf+":/etc/systemd/ukify.conf:ro",
		"--entrypoint", "/bin/sh",
		ref, "-c", ukiScript)
	if err != nil {
		return fmt.Errorf("building the UKI: %w", err)
	}
	if r.dryRun {
		r.echo(nil, "nomira-append-layer", []string{dir, "boot/EFI/Linux/<kver>.efi"})
		return nil
	}
	layer, err := ukiLayer(out)
	if err != nil {
		return err
	}
	return appendLayer(dir, layer, "nomira: unified kernel image")
}

// ukiLayer packs the UKI and whiteouts for the moved kernel files into an
// uncompressed tar. Timestamps are fixed, so the layer depends only on
// its content.
func ukiLayer(out string) ([]byte, error) {
	kernels, err := os.ReadDir(filepath.Join(out, "kernel"))
	if err != nil {
		return nil, err
	}
	if len(kernels) != 1 {
		return nil, fmt.Errorf("expected one kernel version, found %d", len(kernels))
	}
	kver := kernels[0].Name()
	moved, err := os.ReadDir(filepath.Join(out, "kernel", kver))
	if err != nil {
		return nil, err
	}
	uki, err := os.ReadFile(filepath.Join(out, "boot", "EFI", "Linux", kver+".efi"))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	epoch := time.Unix(0, 0)
	for _, d := range []string{"boot/", "boot/EFI/", "boot/EFI/Linux/"} {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d, Mode: 0o755, ModTime: epoch}); err != nil {
			return nil, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "boot/EFI/Linux/" + kver + ".efi",
		Mode: 0o644, Size: int64(len(uki)), ModTime: epoch}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(uki); err != nil {
		return nil, err
	}
	for _, m := range moved {
		wh := path.Join("usr/lib/modules", kver, ".wh."+m.Name())
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: wh, Mode: 0o644, ModTime: epoch}); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const (
	mediaManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaLayerTar = "application/vnd.oci.image.layer.v1.tar"
)

// appendLayer adds an uncompressed layer to the single image of an OCI
// layout directory: it writes the layer, a new config and a new manifest,
// and points index.json at the new manifest. Fields it does not know are
// kept as they are.
func appendLayer(dir string, layer []byte, createdBy string) error {
	indexPath := filepath.Join(dir, "index.json")
	var index map[string]any
	if err := readJSON(indexPath, &index); err != nil {
		return err
	}
	manifests, _ := index["manifests"].([]any)
	if len(manifests) != 1 {
		return fmt.Errorf("%s: expected one image, found %d", indexPath, len(manifests))
	}
	desc, _ := manifests[0].(map[string]any)
	if desc == nil || desc["mediaType"] != mediaManifest {
		return fmt.Errorf("%s: expected an image manifest, found %v", indexPath, desc["mediaType"])
	}
	mdigest, _ := desc["digest"].(string)

	var manifest map[string]any
	if err := readJSON(blobPath(dir, mdigest), &manifest); err != nil {
		return err
	}
	cdesc, _ := manifest["config"].(map[string]any)
	cdigest, _ := cdesc["digest"].(string)
	var config map[string]any
	if err := readJSON(blobPath(dir, cdigest), &config); err != nil {
		return err
	}

	ldigest, err := writeBlob(dir, layer)
	if err != nil {
		return err
	}

	rootfs, _ := config["rootfs"].(map[string]any)
	if rootfs == nil {
		return fmt.Errorf("config %s has no rootfs", cdigest)
	}
	diffIDs, _ := rootfs["diff_ids"].([]any)
	rootfs["diff_ids"] = append(diffIDs, ldigest) // uncompressed: digest = diff ID
	history, _ := config["history"].([]any)
	config["history"] = append(history, map[string]any{"created_by": createdBy})
	cdata, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if cdesc["digest"], err = writeBlob(dir, cdata); err != nil {
		return err
	}
	cdesc["size"] = len(cdata)

	layers, _ := manifest["layers"].([]any)
	manifest["layers"] = append(layers, map[string]any{
		"mediaType": mediaLayerTar, "digest": ldigest, "size": len(layer),
	})
	mdata, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if desc["digest"], err = writeBlob(dir, mdata); err != nil {
		return err
	}
	desc["size"] = len(mdata)

	idata, err := json.Marshal(index)
	if err != nil {
		return err
	}
	tmp := indexPath + ".new"
	if err := os.WriteFile(tmp, idata, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath)
}

func readJSON(file string, v any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// Numbers stay as they are written, sizes included.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

// writeBlob stores data under its sha256 digest and returns the digest.
func writeBlob(dir string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	p := blobPath(dir, digest)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return digest, nil
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	return digest, os.Rename(tmp, p)
}
