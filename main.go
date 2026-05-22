package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
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

func ExtractLayerTGZ(gzipStream io.Reader, targetDir string) error {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer uncompressedStream.Close()

	tarReader := tar.NewReader(uncompressedStream)

	opaqueAttr := "trusted.overlay.opaque"

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

			err := unix.Setxattr(targetDirPath, opaqueAttr, []byte{'y'}, 0)
			if err != nil {
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

			err := unix.Mknod(targetPath, unix.S_IFCHR|0600, 0)
			if err != nil {
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

			flag := os.O_CREATE | os.O_RDWR | os.O_TRUNC
			f, err := os.OpenFile(target, flag, header.FileInfo().Mode())

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
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
		}
	}
	return nil
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
