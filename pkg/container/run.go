package container

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ahmed0427/orca/pkg/image"
	"golang.org/x/sys/unix"
)

type CgroupSpecs struct {
	MemoryMax string
	CPUMax    string
	PidsMax   string
}

type ContainerConfig struct {
	Name    string      `json:"name"`
	Cmd     []string    `json:"args"`
	Env     []string    `json:"env"`
	RootDir string      `json:"root_dir"`
	Limits  CgroupSpecs `json:"limits"`
}

const (
	cgroupPath   = "/sys/fs/cgroup"
	configEnvVar = "CONTAINER_CONFIG"
)

func GenerateHexID() (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func CreatContainerDir() (string, error) {
	id, err := GenerateHexID()
	if err != nil {
		return "", fmt.Errorf("failed to generate hex ID")
	}

	containerPath := image.ContainerPath(id)
	if err := os.MkdirAll(containerPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", containerPath, err)
	}

	return id, nil
}

func MountOverlayFS(id, tag string, config *image.ConfigBlob) error {
	var lowerDirs []string
	for _, diffId := range config.Rootfs.DiffIds {
		layerID := image.LayerID(diffId)
		layerDir := image.LayerPath(layerID)
		if !image.LayerExists(layerID) {
			return fmt.Errorf("%s is corrupted: layer %s don't exists", tag, layerID)
		}
		lowerDirs = append([]string{layerDir}, lowerDirs...)
	}

	containerPath := image.ContainerPath(id)
	upperDir := filepath.Join(containerPath, "upper")
	workDir := filepath.Join(containerPath, "work")
	rootDir := filepath.Join(containerPath, "root")

	for _, dir := range []string{upperDir, workDir, rootDir} {
		os.MkdirAll(dir, 0755)
	}

	mountOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
		strings.Join(lowerDirs, ":"), upperDir, workDir)

	if err := unix.Mount("overlay", rootDir, "overlay", 0, mountOpts); err != nil {
		return fmt.Errorf("overlay mount failed: %v", err)
	}

	return nil
}

func RunImage(tag string, userCmd []string) error {
	manifest, err := image.ReadManifest(tag)
	if err != nil {
		return err
	}
	config, err := image.ReadConfig(manifest.Config.Digest)
	if err != nil {
		return err
	}

	id, err := CreatContainerDir()
	if err != nil {
		return fmt.Errorf("failed to creat container dir: %w", err)
	}

	err = MountOverlayFS(id, tag, config)
	if err != nil {
		return err
	}

	containerPath := image.ContainerPath(id)
	rootDir := filepath.Join(containerPath, "root")

	cmd := config.Config.Cmd
	if len(userCmd) > 0 {
		cmd = userCmd
	}

	Run(id, cmd, config.Config.Env, rootDir, CgroupSpecs{})
	return nil
}

func Run(name string, cmdArgs []string, env []string, rootDir string, limits CgroupSpecs) {
	env = append(env, fmt.Sprintf("TERM=%s", os.Getenv("TERM")))

	config := ContainerConfig{
		Name:    name,
		Cmd:     cmdArgs,
		Env:     env,
		RootDir: rootDir,
		Limits:  limits,
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		log.Fatalf("failed to marshal container config: %v", err)
	}

	exeCmd := exec.Command("/proc/self/exe", "_init_")
	exeCmd.Stdin, exeCmd.Stdout, exeCmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	exeCmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", configEnvVar, string(configBytes)))

	exeCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWNET,
	}

	if err := exeCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("Container runtime error: %v", err)
	}
}

func Init() error {
	configRaw := os.Getenv(configEnvVar)
	if configRaw == "" {
		return fmt.Errorf("container init failed: missing configuration context")
	}
	_ = os.Unsetenv(configEnvVar)

	var config ContainerConfig
	if err := json.Unmarshal([]byte(configRaw), &config); err != nil {
		return fmt.Errorf("container init failed to parse config: %v", err)
	}

	if err := ApplyCgroups(config.Name, config.Limits); err != nil {
		log.Printf("Warning: failed to enforce cgroups: %v", err)
	}

	if err := syscall.Sethostname([]byte(config.Name)); err != nil {
		return fmt.Errorf("failed to set hostname: %v", err)
	}

	if err := syscall.Chroot(config.RootDir); err != nil {
		return fmt.Errorf("failed to chroot to %s: %v", config.RootDir, err)
	}

	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("failed to change directory to root: %v", err)
	}

	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("failed to mount /proc: %v", err)
	}

	defer syscall.Unmount("/proc", 0)

	ExecutePayload(config)
	return nil
}

func ApplyCgroups(name string, limits CgroupSpecs) error {
	cgroupDir := filepath.Join(cgroupPath, "orca", name)
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup directory: %w", err)
	}

	pid := strconv.Itoa(os.Getpid())
	err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(pid), 0644)
	if err != nil {
		return fmt.Errorf("failed to join cgroup: %w", err)
	}

	if limits.CPUMax != "" {
		_ = os.WriteFile(filepath.Join(cgroupDir, "cpu.max"), []byte(limits.CPUMax), 0644)
	}

	pidsLimit := "max"
	if limits.PidsMax != "" {
		pidsLimit = limits.PidsMax
	}
	memoryLimit := "max"
	if limits.MemoryMax != "" {
		memoryLimit = limits.MemoryMax
	}

	_ = os.WriteFile(filepath.Join(cgroupDir, "pids.max"), []byte(pidsLimit), 0644)
	_ = os.WriteFile(filepath.Join(cgroupDir, "memory.max"), []byte(memoryLimit), 0644)

	return nil
}

func ExecutePayload(c ContainerConfig) {
	if len(c.Cmd) == 0 {
		log.Fatalf("Execution failure: no command provided")
	}

	cmd := exec.Command(c.Cmd[0], c.Cmd[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(cmd.Env, c.Env...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("HOSTNAME=%s", c.Name))

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("Execution failure: %v", err)
	}
}
