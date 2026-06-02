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

const BasePath = "/var/orca"

func BlobPath(digest string) string  { return filepath.Join(BasePath, "blobs", digest) }
func LayerPath(id string) string     { return filepath.Join(BasePath, "layers", id) }
func TagPath(tag string) string      { return filepath.Join(BasePath, "tags", tag) }
func ContainerPath(id string) string { return filepath.Join(BasePath, "containers", id) }

func PathExists(path string) bool    { _, err := os.Stat(path); return err == nil }
func BlobExists(digest string) bool  { return PathExists(BlobPath(digest)) }
func LayerExists(id string) bool     { return PathExists(LayerPath(id)) }
func ContainerExists(id string) bool { return PathExists(ContainerPath(id)) }

func LayerID(diffID string) string { return strings.TrimPrefix(diffID, "sha256:")[:12] }

func FullRef(registry, namespace, repo, tag string) string {
	return fmt.Sprintf("%s/%s/%s:%s", registry, namespace, repo, tag)
}

func EncodeRef(ref string) string {
	return hex.EncodeToString([]byte(ref))
}

func DecodeRef(filename string) string {
	decodedBytes, _ := hex.DecodeString(filename)
	return string(decodedBytes)
}

func EnsureDirs() error {
	dirs := []string{
		BasePath,
		filepath.Join(BasePath, "blobs"),
		filepath.Join(BasePath, "layers"),
		filepath.Join(BasePath, "tags"),
		filepath.Join(BasePath, "containers"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}
	return nil
}

func DirSize(path string) (int64, error) {
	var size int64

	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			size += info.Size()
		}

		return nil
	})

	return size, err
}

func ImageSize(ref string) (int64, error) {
	manifest, err := ReadManifest(EncodeRef(ref))
	if err != nil {
		return 0, err
	}

	config, err := ReadConfig(manifest.Config.Digest)
	if err != nil {
		return 0, err
	}

	manifestSize, err := GetManifestSize(EncodeRef(ref))
	if err != nil {
		return 0, err
	}
	configSize, err := GetConfigSize(manifest.Config.Digest)
	if err != nil {
		return 0, err
	}

	var total int64 = manifestSize + configSize

	for _, diffID := range config.Rootfs.DiffIds {
		size, err := DirSize(LayerPath(LayerID(diffID)))
		if err != nil {
			return 0, err
		}

		total += size
	}

	return total, nil
}

func ListImages() ([]string, error) {
	tagsDir := filepath.Join(BasePath, "tags")
	if !PathExists(tagsDir) {
		return nil, fmt.Errorf("tags directory doesn't exist")
	}
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return nil, err
	}
	var images []string
	for _, entry := range entries {
		if !entry.IsDir() {
			images = append(images, DecodeRef(entry.Name()))
		}
	}
	return images, nil
}

func ReadManifest(tag string) (*ManifestResponse, error) {
	manifestPath := TagPath(tag)
	if !PathExists(manifestPath) {
		return nil, fmt.Errorf("image %s not found", tag)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}
	manifest := &ManifestResponse{}
	if err := json.Unmarshal(manifestBytes, manifest); err != nil {
		return nil, fmt.Errorf("image corrupted: invalid manifest JSON: %w", err)
	}

	return manifest, nil
}

func GetManifestSize(tag string) (int64, error) {
	info, err := os.Stat(TagPath(tag))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("image %s not found", tag)
		}
		return 0, fmt.Errorf("failed to get manifest size: %w", err)
	}
	return info.Size(), nil
}

func ReadConfig(digest string) (*ConfigBlob, error) {
	configBytes, err := os.ReadFile(BlobPath(digest))
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	config := &ConfigBlob{}
	if err := json.Unmarshal(configBytes, config); err != nil {
		return nil, fmt.Errorf("image corrupted: invalid config JSON: %w", err)
	}
	return config, nil
}

func GetConfigSize(digest string) (int64, error) {
	info, err := os.Stat(BlobPath(digest))
	if err != nil {
		return 0, fmt.Errorf("failed to get config file size: %w", err)
	}
	return info.Size(), nil
}

func VerifyImage(ref string) error {
	tag := EncodeRef(ref)
	manifest, err := ReadManifest(tag)
	if err != nil {
		return err
	}

	configFile, err := os.Open(BlobPath(manifest.Config.Digest))
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

	calculatedDigest := hex.EncodeToString(hash.Sum(nil))
	if calculatedDigest != strings.TrimPrefix(manifest.Config.Digest, "sha256:") {
		return fmt.Errorf("image corrupted: config hash mismatch")
	}

	config, err := ReadConfig(manifest.Config.Digest)
	if err != nil {
		return err
	}

	for _, diffID := range config.Rootfs.DiffIds {
		id := LayerID(diffID)
		if !PathExists(LayerPath(id)) {
			return fmt.Errorf("image corrupted: missing layer directory with ID: %s", id)
		}
	}
	return nil
}

func RemoveImage(ref string) error {
	p := TagPath(EncodeRef(ref))
	if !PathExists(p) {
		return fmt.Errorf("image tag %s does not exist", ref)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("failed to remove tag %s: %w", ref, err)
	}
	fmt.Printf("Untagged: %s\n", ref)
	deleted, err := GarbageCollectVerbose()
	if err != nil {
		return fmt.Errorf("garbage collection failed: %w", err)
	}
	for _, d := range deleted {
		fmt.Printf("Deleted: %s\n", d)
	}

	return nil
}

func GarbageCollect() error {
	_, err := GarbageCollectVerbose()
	return err
}

func GarbageCollectVerbose() ([]string, error) {
	deleted := make([]string, 0)
	activeBlobs := make(map[string]bool)
	activeLayers := make(map[string]bool)

	tagsDir := filepath.Join(BasePath, "tags")
	if !PathExists(tagsDir) {
		return nil, nil
	}

	tags, err := os.ReadDir(tagsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags directory: %w", err)
	}

	for _, tagFile := range tags {
		if tagFile.IsDir() {
			continue
		}
		ref := DecodeRef(tagFile.Name())
		if err := VerifyImage(ref); err != nil {
			continue
		}
		manifest, err := ReadManifest(tagFile.Name())
		if err != nil {
			continue
		}

		activeBlobs[manifest.Config.Digest] = true
		config, err := ReadConfig(manifest.Config.Digest)
		if err != nil {
			continue
		}

		for _, diffID := range config.Rootfs.DiffIds {
			activeLayers[LayerID(diffID)] = true
		}
	}

	blobsDir := filepath.Join(BasePath, "blobs")
	if PathExists(blobsDir) {
		blobs, err := os.ReadDir(blobsDir)
		if err == nil {
			for _, b := range blobs {
				if !activeBlobs[b.Name()] {
					_ = os.Remove(filepath.Join(blobsDir, b.Name()))
				}
			}
		}
	}

	layersDir := filepath.Join(BasePath, "layers")
	if PathExists(layersDir) {
		layers, err := os.ReadDir(layersDir)
		if err == nil {
			for _, l := range layers {
				if !activeLayers[l.Name()] {
					err = os.RemoveAll(filepath.Join(layersDir, l.Name()))
					if err == nil {
						deleted = append(deleted, l.Name())
					}
				}
			}
		}
	}
	return deleted, nil
}
