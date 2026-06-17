package container

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ahmed0427/orca/pkg/image"
)

func ListContainers() ([]string, error) {
	containersDir := filepath.Join(image.BasePath, "containers")
	if !image.PathExists(containersDir) {
		return nil, fmt.Errorf("containers directory doesn't exist")
	}
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return nil, err
	}

	var containers []string
	for _, entry := range entries {
		containers = append(containers, entry.Name())
	}
	return containers, nil
}
