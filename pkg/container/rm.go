package container

import (
	"errors"
	"fmt"
	"io/fs"
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
	parentCgroupProcs := filepath.Join(CgroupRoot, "orca", "cgroup.procs")

	pidBytes, err := os.ReadFile(filepath.Join(containerPath, "container.pid"))
	if err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			EnsureTerminated(pid)
		} else {
			panic("container dir corrupted")
		}
	}

	procTarget := filepath.Join(rootDir, "proc")
	if err := syscall.Unmount(procTarget, syscall.MNT_DETACH); err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.EINVAL) {
			return fmt.Errorf("failed to unmount procfs: %v", err)
		}
	}

	if err := syscall.Unmount(rootDir, syscall.MNT_DETACH); err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.EINVAL) {
			return fmt.Errorf("failed to unmount overlay root: %v", err)
		}
	}

	var cgroupRemoveErr error
	for i := 0; i < 15; i++ {
		data, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.procs"))
		if err == nil {
			pids := strings.Fields(string(data))
			for _, pidStr := range pids {
				if pid, err := strconv.Atoi(pidStr); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)

					_ = os.WriteFile(parentCgroupProcs, []byte(pidStr), 0644)
				}
			}
		}

		cgroupRemoveErr = os.Remove(cgroupPath)
		if cgroupRemoveErr == nil {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if cgroupRemoveErr != nil {
		return fmt.Errorf("failed to remove cgroup dir: %v", cgroupRemoveErr)
	}

	if err := os.RemoveAll(containerPath); err != nil {
		return fmt.Errorf("failed to remove container directory: %w", err)
	}

	CleanupContainer(id)

	return nil
}

func EnsureTerminated(pid int) {
	for i := 0; i < 25; i++ {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return // ESRCH means the process is completely dead and gone
		}
		time.Sleep(100 * time.Millisecond)
	}
}
