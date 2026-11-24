package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// descriptor is the part of an OCI descriptor that pruning needs.
type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}

// ociDoc covers index and manifest documents alike.
type ociDoc struct {
	MediaType string       `json:"mediaType"`
	Manifests []descriptor `json:"manifests"`
	Config    *descriptor  `json:"config"`
	Layers    []descriptor `json:"layers"`
}

// referenced returns the digests of all blobs the index of a layout
// directory needs: manifests, nested indexes, configs and layers.
func referenced(dir string) (map[string]bool, error) {
	keep := map[string]bool{}
	var walk func(digest string) error
	walk = func(digest string) error {
		if keep[digest] {
			return nil
		}
		keep[digest] = true
		data, err := os.ReadFile(blobPath(dir, digest))
		if err != nil {
			return err
		}
		var doc ociDoc
		if json.Unmarshal(data, &doc) != nil {
			return nil // not JSON: a layer
		}
		for _, d := range doc.Manifests {
			if err := walk(d.Digest); err != nil {
				return err
			}
		}
		if doc.Config != nil {
			keep[doc.Config.Digest] = true
		}
		for _, l := range doc.Layers {
			keep[l.Digest] = true
		}
		return nil
	}

	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var index ociDoc
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	for _, d := range index.Manifests {
		if err := walk(d.Digest); err != nil {
			return nil, err
		}
	}
	return keep, nil
}

// pruneLayout deletes the blobs of an OCI layout directory that the current
// index no longer references. BuildKit keeps old blobs when it exports into
// an existing directory, which is what makes the export incremental, but
// without pruning the directory would grow with every build.
func pruneLayout(dir string) error {
	keep, err := referenced(dir)
	if err != nil {
		return err
	}
	algos, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		return err
	}
	for _, a := range algos {
		blobs, err := os.ReadDir(filepath.Join(dir, "blobs", a.Name()))
		if err != nil {
			return err
		}
		for _, b := range blobs {
			if !keep[a.Name()+":"+b.Name()] && !strings.HasSuffix(b.Name(), ".new") {
				if err := os.Remove(filepath.Join(dir, "blobs", a.Name(), b.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func blobPath(dir, digest string) string {
	algo, hex, _ := strings.Cut(digest, ":")
	return filepath.Join(dir, "blobs", algo, hex)
}

// publish makes the image in the layout directory work available in dir:
// the blobs it needs are linked (or copied) over, then index.json is
// replaced in one step, so dir always holds a complete image. Readers of
// dir, bootc upgrade among them, never see an image that is still being
// built or checked.
func publish(work, dir string) error {
	need, err := referenced(work)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for digest := range need {
		dst := blobPath(dir, digest)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		src := blobPath(work, digest)
		if os.Link(src, dst) == nil {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	// index.json last: until it is replaced, dir points at the old image.
	for _, f := range []string{"oci-layout", "index.json"} {
		if err := copyFile(filepath.Join(work, f), filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	return pruneLayout(dir)
}

// copyFile copies src to dst through a temporary file next to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
