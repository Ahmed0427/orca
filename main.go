package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	basePath = "/var/orca"
)

func ensureDirs() error {
	dirs := []string{
		basePath,
		filepath.Join(basePath, "blobs"),
		filepath.Join(basePath, "layers"),
		filepath.Join(basePath, "tags"),
		filepath.Join(basePath, "containers"),
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}
	return nil
}

func blobExists(digest string) bool {
	blobPath := filepath.Join(basePath, "blobs", digest)
	info, err := os.Stat(blobPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func pull(target string) error {
	namespace, repo, tag := parseImageTarget(target)

	client, err := NewClient(namespace, repo)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	manifestResult, err := client.GetManifest(tag)
	if err != nil {
		return fmt.Errorf("failed to get manifest: %w", err)
	}

	manifestPath := filepath.Join(basePath, "tags", fmt.Sprintf("%s:%s", repo, tag))
	if err := os.WriteFile(manifestPath, manifestResult.RawBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	confDigest := manifestResult.Manifest.Config.Digest
	if !blobExists(confDigest) {
		conf, err := client.GetConfig(confDigest, manifestResult.Manifest.Config.Size)
		if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		confBytes, err := json.Marshal(conf)
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}

		confPath := filepath.Join(basePath, "blobs", confDigest)
		if err := os.WriteFile(confPath, confBytes, 0644); err != nil {
			return fmt.Errorf("failed to write config blob: %w", err)
		}
	}

	pre := []string{fmt.Sprintf("Pulling from %s/%s with tag=%s", namespace, repo, tag)}
	post := []string{
		fmt.Sprintf("Digest: %s", manifestResult.Digest),
		fmt.Sprintf("Status: Downloaded newer image for %s:%s", repo, tag),
	}

	mp := NewMultiProgress(pre, post)

	for _, l := range manifestResult.Manifest.Layers {
		if !blobExists(l.Digest) {
			mp.AddTask(strings.TrimPrefix(l.Digest, "sha256:")[:12], l.Digest, l.Size)
		}
	}

	mp.Render()

	for _, task := range mp.Tasks {
		err := func() error {
			layerPath := filepath.Join(basePath, "blobs", task.ID)
			dst, err := os.Create(layerPath)
			if err != nil {
				return fmt.Errorf("failed to create layer file %s: %w", layerPath, err)
			}
			defer dst.Close()

			proxy := ProgressProxy{
				Task:   task,
				Layout: mp,
				Writer: dst,
			}

			if err := client.DownloadLayer(task.ID, task.Total, proxy); err != nil {
				return fmt.Errorf("failed to download layer %s: %w", task.ID, err)
			}
			return nil
		}()

		if err != nil {
			return err
		}
	}

	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <repository:tag>\n", os.Args[0])
		fmt.Printf("Example: %s library/alpine:latest\n", os.Args[0])
		os.Exit(1)
	}

	if err := ensureDirs(); err != nil {
		log.Fatalf("Initialization failed: %v", err)
	}

	if err := pull(os.Args[1]); err != nil {
		log.Fatalf("Pull failed: %v", err)
	}
}
