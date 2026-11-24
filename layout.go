package main

import (
	"encoding/json"
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

// pruneLayout deletes the blobs of an OCI layout directory that the current
// index no longer references. BuildKit keeps old blobs when it exports into
// an existing directory, which is what makes the export incremental, but
// without pruning the directory would grow with every build.
func pruneLayout(dir string) error {
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
		return err
	}
	var index ociDoc
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}
	for _, d := range index.Manifests {
		if err := walk(d.Digest); err != nil {
			return err
		}
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
			if !keep[a.Name()+":"+b.Name()] {
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
