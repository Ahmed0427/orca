package container

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ahmed0427/orca/pkg/image"
)

func RemoveContainer(id string) error {
	containerPath := image.ContainerPath(id)
	rootDir := filepath.Join(containerPath, "root")
	cgroupPath := filepath.Join(CgroupRoot, "orca", id)
	pidBytes, err := os.ReadFile(filepath.Join(containerPath, "container.pid"))
	if err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			EnsureTerminated(pid)
		} else {
			panic("conatiner dir corrupted")
		}
	}

	procTarget := filepath.Join(rootDir, "proc")
	if err := syscall.Unmount(procTarget, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("failed to unmount procfs: %v", err)
	}

	if err := syscall.Unmount(rootDir, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("failed to unmount overlay root: %v", err)
	}

	if err := os.RemoveAll(cgroupPath); err != nil {
		return fmt.Errorf("failed to remove cgroup dir: %v", err)
	}

	if err := os.RemoveAll(containerPath); err != nil {
		return fmt.Errorf("failed to remove container directory: %w", err)
	}

	return nil
}

func EnsureTerminated(pid int) {
	for i := 0; i < 10; i++ { // try for up to 5 seconds
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return // ESRCH means process is officially gone
		}
		time.Sleep(100 * time.Millisecond)
	}
}
