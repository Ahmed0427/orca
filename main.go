package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const basePath = "/var/orca"

func blobPath(digest string) string {
	return filepath.Join(basePath, "blobs", digest)
}
func layerPath(id string) string {
	return filepath.Join(basePath, "layers", id)
}
func tagPath(tag string) string {
	return filepath.Join(basePath, "tags", tag)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func blobExists(digest string) bool {
	return pathExists(blobPath(digest))
}
func layerExists(id string) bool {
	return pathExists(layerPath(id))
}

func layerID(diffID string) string {
	return strings.TrimPrefix(diffID, "sha256:")[:12]
}

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

func deleteBlob(digest string) error {
	err := os.Remove(blobPath(digest))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func deleteLayer(id string) error {
	return os.RemoveAll(layerPath(id))
}

func verifyImage(tag string) error {
	manifestPath := tagPath(tag)
	if !pathExists(manifestPath) {
		return fmt.Errorf("image %s not found", tag)
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}
	var manifest ManifestResponse
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
	fmt.Println(calculatedDigest)
	fmt.Println(manifest.Config.Digest)
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

func removeImage(tag string) error {
	p := tagPath(tag)
	if !pathExists(p) {
		return fmt.Errorf("image tag %s does not exist", tag)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("failed to remove tag %s: %w", tag, err)
	}
	err := garbageCollect()
	fmt.Printf("%s image removed successfully\n", tag)
	return err
}

func garbageCollect() error {
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
		for _, l := range manifest.Layers {
			deleteBlob(l.Digest)
		}
	}

	blobsDir := filepath.Join(basePath, "blobs")
	if pathExists(blobsDir) {
		blobs, err := os.ReadDir(blobsDir)
		if err != nil {
			return err
		}
		for _, b := range blobs {
			if !activeBlobs[b.Name()] {
				_ = os.Remove(filepath.Join(blobsDir, b.Name()))
			}
		}
	}

	layersDir := filepath.Join(basePath, "layers")
	if pathExists(layersDir) {
		layers, err := os.ReadDir(layersDir)
		if err != nil {
			return err
		}
		for _, l := range layers {
			if !activeLayers[l.Name()] {
				_ = os.RemoveAll(filepath.Join(layersDir, l.Name()))
			}
		}
	}

	return nil
}

func extractLayerTGZ(compressedStream io.Reader, targetDir string) error {
	uncompressedStream, err := gzip.NewReader(compressedStream)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer uncompressedStream.Close()

	tarReader := tar.NewReader(uncompressedStream)
	const opaqueAttr = "trusted.overlay.opaque"

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		cleanPath := filepath.Clean(header.Name)
		baseName := filepath.Base(cleanPath)
		dirName := filepath.Dir(cleanPath)
		targetDirPath := filepath.Join(targetDir, dirName)

		if baseName == ".wh..wh..opq" {
			if err := os.MkdirAll(targetDirPath, 0755); err != nil {
				return fmt.Errorf("failed to create parent for opaque directory: %w", err)
			}
			if err := unix.Setxattr(targetDirPath, opaqueAttr, []byte{'y'}, 0); err != nil {
				return fmt.Errorf("failed to set opaque xattr on %s: %w", targetDirPath, err)
			}
			continue
		}

		if strings.HasPrefix(baseName, ".wh.") {
			realFileName := strings.TrimPrefix(baseName, ".wh.")
			targetPath := filepath.Join(targetDirPath, realFileName)
			if err := os.MkdirAll(targetDirPath, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for whiteout: %w", err)
			}
			if err := unix.Mknod(targetPath, unix.S_IFCHR|0600, 0); err != nil {
				return fmt.Errorf("failed to create whiteout device at %s: %w", targetPath, err)
			}
			continue
		}

		target := filepath.Join(targetDir, cleanPath)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, header.FileInfo().Mode()); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(targetDirPath, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}
			if _, err := io.Copy(f, tarReader); err != nil {
				f.Close()
				return fmt.Errorf("failed to copy file contents: %w", err)
			}
			f.Close()

		case tar.TypeSymlink:
			if err := os.MkdirAll(targetDirPath, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}

		case tar.TypeLink:
			old := filepath.Join(targetDir, header.Linkname)
			if err := os.MkdirAll(targetDirPath, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			if err := os.Link(old, target); err != nil {
				return fmt.Errorf("failed to create link from %s to %s: %w", old, target, err)
			}
		}
	}
	return nil
}

func pullImage(target string) error {
	namespace, repo, tag := parseImageTarget(target)

	client, err := NewClient(namespace, repo)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	manifestResult, err := client.GetManifest(tag)
	if err != nil {
		return fmt.Errorf("failed to get manifest: %w", err)
	}

	manifestPath := tagPath(fmt.Sprintf("%s:%s", repo, tag))
	if err := os.WriteFile(manifestPath, manifestResult.RawBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	confDigest := manifestResult.Manifest.Config.Digest
	conf, confBytes, err := client.GetConfig(confDigest, manifestResult.Manifest.Config.Size)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	if err := os.WriteFile(blobPath(confDigest), confBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config blob: %w", err)
	}

	pre := []string{fmt.Sprintf("Pulling from %s/%s with tag=%s", namespace, repo, tag)}
	post := []string{
		fmt.Sprintf("Digest: %s", manifestResult.Digest),
		fmt.Sprintf("Status: Downloaded newer image for %s:%s", repo, tag),
	}
	mp := NewMultiProgress(pre, post)

	for _, l := range manifestResult.Manifest.Layers {
		if !blobExists(l.Digest) {
			title := strings.TrimPrefix(l.Digest, "sha256:")[:12]
			mp.AddTask(title, l.Digest, l.Size)
		}
	}
	mp.Render()

	for _, task := range mp.Tasks {
		err := func() error {
			dst, err := os.Create(blobPath(task.ID))
			if err != nil {
				return fmt.Errorf("failed to create layer file: %w", err)
			}
			defer dst.Close()

			proxy := ProgressProxy{Task: task, Layout: mp, Writer: dst}
			if err := client.DownloadLayer(task.ID, task.Total, proxy); err != nil {
				return fmt.Errorf("failed to download layer %s: %w", task.ID, err)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(manifestResult.Manifest.Layers))

	fmt.Print("Extracting layers... ")

	for i, l := range manifestResult.Manifest.Layers {
		wg.Add(1)
		go func(index int, layerDigest string) {
			defer wg.Done()

			id := layerID(conf.Rootfs.DiffIds[index])
			if layerExists(id) {
				return
			}

			f, err := os.Open(blobPath(layerDigest))
			if err != nil {
				errChan <- err
				return
			}
			defer f.Close()

			if err := extractLayerTGZ(f, layerPath(id)); err != nil {
				errChan <- err
				return
			}
			if err := deleteBlob(layerDigest); err != nil {
				errChan <- err
			}
		}(i, l.Digest)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			return fmt.Errorf("extraction error: %w", err)
		}
	}

	fmt.Println("Done")
	return nil
}

func main() {
	if err := ensureDirs(); err != nil {
		log.Fatalf("initialization failed: %v", err)
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [args...]\n", os.Args[0])
		os.Exit(1)
	}

	switch os.Args[1] {
	case "pull":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s pull <image>\n", os.Args[0])
			os.Exit(1)
		}
		if err := pullImage(os.Args[2]); err != nil {
			log.Fatalf("pull failed: %v", err)
		}
	case "rm":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s rm <image:tag>\n", os.Args[0])
			os.Exit(1)
		}
		if err := removeImage(os.Args[2]); err != nil {
			log.Fatalf("remove failed: %v", err)
		}
	case "verify":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s verify <image:tag>\n", os.Args[0])
			os.Exit(1)
		}

		err := verifyImage(os.Args[2])
		if err != nil {
			if strings.HasPrefix(err.Error(), "image corrupted:") {
				fmt.Printf("Critical: %v\n", err)
				fmt.Printf("Run '%s rm %s' to remove it, then re-download.\n", os.Args[0], os.Args[2])
				os.Exit(1)
			} else {
				log.Fatalf("Failed to complete verification: %v\n", err)
			}
		}
		fmt.Println("Image is fine")
	case "images":
		tagsDir := filepath.Join(basePath, "tags")
		if !pathExists(tagsDir) {
			log.Fatalf("tags directory doesn't exist\n")
		}
		tags, err := os.ReadDir(tagsDir)
		if err != nil {
			log.Fatalf("failed to read tags directory: %v\n", err)
		}
		for _, tagFile := range tags {
			fmt.Println(tagFile.Name())
		}
	case "gc":
		garbageCollect()
	default:
		fmt.Fprintf(os.Stderr, "no such command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
