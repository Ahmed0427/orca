package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const basePath = "/var/orca"

// Helper paths
func blobPath(digest string) string { return filepath.Join(basePath, "blobs", digest) }
func layerPath(id string) string    { return filepath.Join(basePath, "layers", id) }
func tagPath(tag string) string     { return filepath.Join(basePath, "tags", tag) }

func pathExists(path string) bool   { _, err := os.Stat(path); return err == nil }
func blobExists(digest string) bool { return pathExists(blobPath(digest)) }
func layerExists(id string) bool    { return pathExists(layerPath(id)) }
func layerID(diffID string) string  { return strings.TrimPrefix(diffID, "sha256:")[:12] }

func EnsureDirs() error {
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

func ListImages() ([]string, error) {
	tagsDir := filepath.Join(basePath, "tags")
	if !pathExists(tagsDir) {
		return nil, fmt.Errorf("tags directory doesn't exist")
	}
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return nil, err
	}
	var images []string
	for _, entry := range entries {
		if !entry.IsDir() {
			images = append(images, entry.Name())
		}
	}
	return images, nil
}

func VerifyImage(tag string) error {
	manifestPath := tagPath(tag)
	if !pathExists(manifestPath) {
		return fmt.Errorf("image %s not found", tag)
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}
	var manifest ManifestResponse // Assumes defined in client.go or shared
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("image corrupted: invalid manifest JSON: %w", err)
	}

	configFile, err := os.Open(blobPath(manifest.Config.Digest))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("image corrupted: config blob missing: %w", err)
		}
		return fmt.Errorf("failed to open config blob: %w", err)
	}
	defer configFile.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, configFile); err != nil {
		return fmt.Errorf("failed to hash config file: %w", err)
	}
	calculatedDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if calculatedDigest != manifest.Config.Digest {
		return fmt.Errorf("image corrupted: config hash mismatch")
	}

	if _, err := configFile.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek config file: %w", err)
	}
	configBytes, err := io.ReadAll(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	var config ConfigBlob
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return fmt.Errorf("image corrupted: invalid config JSON: %w", err)
	}

	for _, diffID := range config.Rootfs.DiffIds {
		id := layerID(diffID)
		if !pathExists(layerPath(id)) {
			return fmt.Errorf("image corrupted: missing layer directory with ID: %s", id)
		}
	}
	return nil
}

func RemoveImage(tag string) error {
	p := tagPath(tag)
	if !pathExists(p) {
		return fmt.Errorf("image tag %s does not exist", tag)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("failed to remove tag %s: %w", tag, err)
	}
	fmt.Printf("%s image removed successfully\n", tag)
	return GarbageCollect()
}

func GarbageCollect() error {
	activeBlobs := make(map[string]bool)
	activeLayers := make(map[string]bool)

	tagsDir := filepath.Join(basePath, "tags")
	if !pathExists(tagsDir) {
		return nil
	}

	tags, err := os.ReadDir(tagsDir)
	if err != nil {
		return fmt.Errorf("failed to read tags directory: %w", err)
	}

	for _, tagFile := range tags {
		if tagFile.IsDir() {
			continue
		}
		if err := VerifyImage(tagFile.Name()); err != nil {
			continue
		}
		manifestBytes, err := os.ReadFile(filepath.Join(tagsDir, tagFile.Name()))
		if err != nil {
			continue
		}
		var manifest ManifestResponse
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			continue
		}
		activeBlobs[manifest.Config.Digest] = true

		configBytes, err := os.ReadFile(blobPath(manifest.Config.Digest))
		if err != nil {
			continue
		}
		var config ConfigBlob
		if err := json.Unmarshal(configBytes, &config); err != nil {
			continue
		}
		for _, diffID := range config.Rootfs.DiffIds {
			activeLayers[layerID(diffID)] = true
		}
	}

	// Clean unreferenced blobs and layers
	blobsDir := filepath.Join(basePath, "blobs")
	if pathExists(blobsDir) {
		blobs, err := os.ReadDir(blobsDir)
		if err == nil {
			for _, b := range blobs {
				if !activeBlobs[b.Name()] {
					_ = os.Remove(filepath.Join(blobsDir, b.Name()))
				}
			}
		}
	}

	layersDir := filepath.Join(basePath, "layers")
	if pathExists(layersDir) {
		layers, err := os.ReadDir(layersDir)
		if err == nil {
			for _, l := range layers {
				if !activeLayers[l.Name()] {
					_ = os.RemoveAll(filepath.Join(layersDir, l.Name()))
				}
			}
		}
	}
	return nil
}
