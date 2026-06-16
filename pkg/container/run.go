package container

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ahmed0427/orca/pkg/image"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type CgroupSpecs struct {
	MemoryMax, CPUMax, PidsMax string
}

type RunOptions struct {
	Interactive bool
	TTY         bool
	Detach      bool
	Name        string
	Hostname    string
	Limits      CgroupSpecs
}

type ContainerConfig struct {
	Name     string
	Hostname string
	Cmd      []string
	Env      []string
	RootDir  string
	Limits   CgroupSpecs
	TTY      bool
}

const CgroupRoot = "/sys/fs/cgroup"
const Cloneflags = syscall.CLONE_NEWNS |
	syscall.CLONE_NEWUTS |
	syscall.CLONE_NEWPID |
	syscall.CLONE_NEWNET |
	syscall.CLONE_NEWIPC

var (
	SentinelShim = "_shim_"
	SentinelInit = "_init_"
)

func RunImage(tag string, userCmd []string, opts RunOptions) error {
	if opts.Detach && opts.Interactive {
		return fmt.Errorf("cannot combine -d with -i")
	}

	manifest, err := image.ReadManifest(image.EncodeRef(tag))
	if err != nil {
		return err
	}
	cfg, err := image.ReadConfig(manifest.Config.Digest)
	if err != nil {
		return err
	}

	id, err := CreateContainerDir()
	if err != nil {
		return err
	}
	if err := MountOverlay(id, tag, cfg); err != nil {
		return err
	}

	containerPath := image.ContainerPath(id)
	rootDir := filepath.Join(containerPath, "root")

	cmd := cfg.Config.Cmd
	if len(userCmd) > 0 {
		cmd = userCmd
	}
	env := append(cfg.Config.Env, "TERM="+os.Getenv("TERM"))

	name := opts.Name
	if name == "" {
		name = id
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = name
	}

	cc := ContainerConfig{
		Name:     name,
		Hostname: hostname,
		Cmd:      cmd,
		Env:      env,
		RootDir:  rootDir,
		Limits:   opts.Limits,
		TTY:      opts.TTY,
	}

	configPath := filepath.Join(containerPath, "config.json")
	b, _ := json.Marshal(cc)
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		return err
	}

	if opts.Detach {
		return StartDetached(id, cc)
	}
	return StartAttached(id, cc, opts)
}

func StartAttached(id string, cc ContainerConfig, opts RunOptions) error {
	exeCmd := exec.Command("/proc/self/exe", SentinelInit, id)
	exeCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: Cloneflags,
	}

	var err error
	var ptm, pts *os.File
	usePTY := opts.Interactive && opts.TTY

	if usePTY {
		ptm, pts, err = pty.Open()
		if err != nil {
			return fmt.Errorf("pty open: %w", err)
		}
		exeCmd.Stdin = pts
		exeCmd.Stdout = pts
		exeCmd.Stderr = pts

		exeCmd.SysProcAttr.Setsid = true
		exeCmd.SysProcAttr.Setctty = true

		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			pts.Close()
			ptm.Close()
			return fmt.Errorf("term raw: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)

		if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
			pty.Setsize(ptm, ws)
		}
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		defer signal.Stop(sigCh)
		go func() {
			for range sigCh {
				if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
					pty.Setsize(ptm, ws)
				}
			}
		}()

		// close ptm after container exits to unblock I/O copies.
		// Remove the defer ptm.Close() so we can control timing.
		// Start the I/O pumps
		go io.Copy(ptm, os.Stdin)
		go io.Copy(os.Stdout, ptm)

		if err := exeCmd.Start(); err != nil {
			pts.Close()
			ptm.Close()
			return fmt.Errorf("start container: %w", err)
		}

		// Wait for the container process
		err = exeCmd.Wait()

		// now close the master, this will cause io.Copy goroutines to receive EOF/error
		ptm.Close()
		pts.Close()

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		return nil

	} else {
		if opts.Interactive {
			exeCmd.Stdin = os.Stdin
		} else {
			exeCmd.Stdin, _ = os.Open(os.DevNull)
		}
		exeCmd.Stdout = os.Stdout
		exeCmd.Stderr = os.Stderr

		if err := exeCmd.Start(); err != nil {
			fmt.Println("HERE")
			return fmt.Errorf("start container: %w", err)
		}
		sigChan := make(chan os.Signal, 32)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
		defer signal.Stop(sigChan)
		go func() {
			for sig := range sigChan {
				exeCmd.Process.Signal(sig)
			}
		}()

		err := exeCmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		return nil
	}
}

func StartDetached(id string, cc ContainerConfig) error {
	stateDir := image.ContainerPath(id)

	shimLog, err := os.Create(filepath.Join(stateDir, "shim.log"))
	if err != nil {
		return err
	}

	shimCmd := exec.Command("/proc/self/exe", SentinelShim, id)
	shimCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	shimCmd.Stdout = shimLog
	shimCmd.Stderr = shimLog

	if err := shimCmd.Start(); err != nil {
		return err
	}
	shimCmd.Process.Release() // reparent to init

	fmt.Println(id)
	return nil
}

func RunShim(id string) error {
	stateDir := image.ContainerPath(id)

	configBytes, err := os.ReadFile(filepath.Join(stateDir, "config.json"))
	if err != nil {
		return fmt.Errorf("shim config: %w", err)
	}
	var cc ContainerConfig
	if err := json.Unmarshal(configBytes, &cc); err != nil {
		return err
	}

	outLog, err := os.OpenFile(filepath.Join(stateDir, "output.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)

	if err != nil {
		return err
	}
	defer outLog.Close()

	devNull, _ := os.Open(os.DevNull)
	exeCmd := exec.Command("/proc/self/exe", SentinelInit, id)
	exeCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: Cloneflags,
	}
	exeCmd.Stdin = devNull
	exeCmd.Stdout = outLog
	exeCmd.Stderr = outLog

	if err := exeCmd.Start(); err != nil {
		return err
	}
	pidPath := filepath.Join(stateDir, "container.pid")
	os.WriteFile(pidPath, []byte(strconv.Itoa(exeCmd.Process.Pid)), 0644)

	err = exeCmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return os.WriteFile(filepath.Join(stateDir, "exit-code"),
		[]byte(strconv.Itoa(exitCode)), 0644)
}

func Init(id string) error {
	configPath := fmt.Sprintf("%v/config.json", image.ContainerPath(id))
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg ContainerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	if err := ApplyCgroups(cfg.Name, cfg.Limits); err != nil {
		log.Printf("warning: cgroup apply failed: %v", err)
	}

	if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
		return err
	}

	procTarget := filepath.Join(cfg.RootDir, "proc")
	if err := syscall.Mount("proc", procTarget, "proc", 0, ""); err != nil {
		return err
	}

	if err := ChangeRoot(cfg.RootDir); err != nil {
		return err
	}

	path, err := exec.LookPath(cfg.Cmd[0])
	if err != nil {
		log.Fatalf("command not found: %v", err)
	}
	return syscall.Exec(path, cfg.Cmd, append(cfg.Env, "HOSTNAME="+cfg.Hostname))
}

func ChangeRoot(newRoot string) error {
	if err := syscall.Chroot(newRoot); err != nil {
		return fmt.Errorf("chroot failed: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir after chroot failed: %w", err)
	}
	return nil
}

func ApplyCgroups(name string, limits CgroupSpecs) error {
	parent := filepath.Join(CgroupRoot, "orca")
	leaf := filepath.Join(parent, name)

	os.MkdirAll(parent, 0755)
	os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"),
		[]byte("+cpu +memory +pids"), 0644)

	os.Mkdir(leaf, 0755)

	write := func(file, val string) {
		os.WriteFile(filepath.Join(leaf, file), []byte(val), 0644)
	}
	write("memory.max", IfEmpty(limits.MemoryMax, "max"))
	write("pids.max", IfEmpty(limits.PidsMax, "max"))
	if limits.CPUMax != "" {
		write("cpu.max", limits.CPUMax)
	}
	return os.WriteFile(filepath.Join(leaf, "cgroup.procs"),
		[]byte(strconv.Itoa(os.Getpid())), 0644)
}

func CreateContainerDir() (string, error) {
	id := GenHexID()
	path := image.ContainerPath(id)
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	return id, nil
}

func MountOverlay(id, tag string, cfg *image.ConfigBlob) error {
	var lowers []string
	for _, diffID := range cfg.Rootfs.DiffIds {
		layerID := image.LayerID(diffID)
		if !image.LayerExists(layerID) {
			return fmt.Errorf("layer %s missing", layerID)
		}
		lowers = append([]string{image.LayerPath(layerID)}, lowers...)
	}
	containerPath := image.ContainerPath(id)
	upper := filepath.Join(containerPath, "upper")
	work := filepath.Join(containerPath, "work")
	root := filepath.Join(containerPath, "root")
	for _, d := range []string{upper, work, root} {
		os.MkdirAll(d, 0755)
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
		strings.Join(lowers, ":"), upper, work)
	return unix.Mount("overlay", root, "overlay", 0, opts)
}

func GenHexID() string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], uint32(time.Now().Unix()))
	_, err := rand.Read(b[4:8])
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func IfEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
