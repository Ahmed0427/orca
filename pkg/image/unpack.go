package image

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func ExtractLayerTGZ(compressedStream io.Reader, targetDir string) error {
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
