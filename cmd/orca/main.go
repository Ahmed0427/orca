package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ahmed0427/orca/pkg/container"
	"github.com/ahmed0427/orca/pkg/image"
)

func main() {
	if err := image.EnsureDirs(); err != nil {
		log.Fatalf("initialization failed: %v", err)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	// internal sentinel commands
	switch cmd {
	case "_init_":
		if err := container.Init(os.Args[2]); err != nil {
			log.Printf("_init_ failed: %v", err)
			os.Exit(1)
		}
		return
	case "_shim_":
		if len(os.Args) < 3 {
			log.Fatal("_shim_ requires container id")
		}
		if err := container.RunShim(os.Args[2]); err != nil {
			log.Printf("_shim_ failed: %v", err)
			os.Exit(1)
		}
		return
	}

	// user facing commands
	switch cmd {
	case "run":
		runCommand(os.Args[2:])
	case "pull":
		pullCommand(os.Args[2:])
	case "rm":
		rmCommand(os.Args[2:])
	case "verify":
		verifyCommand(os.Args[2:])
	case "images":
		imagesCommand(os.Args[2:])
	case "containers":
		containersCommand(os.Args[2:])
	case "gc":
		gcCommand(os.Args[2:])
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func runCommand(args []string) {
	flags := flag.NewFlagSet("run", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s run [OPTIONS] IMAGE [COMMAND...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flags.PrintDefaults()
	}

	interactive := flags.Bool("i", true, "Keep stdin open even when not attached")
	tty := flags.Bool("t", true, "Allocate a pseudo-TTY")
	detach := flags.Bool("d", false, "Run container in background and print container ID")
	name := flags.String("name", "", "Assign a name to the container")
	hostname := flags.String("hostname", "", "Container host name")
	memory := flags.String("memory", "", "Memory limit (e.g. 256m)")
	cpu := flags.String("cpu", "", "CPU limit (e.g. 1.5 or 0.5)")
	pids := flags.String("pids", "", "Maximum number of processes")

	flags.Parse(args)

	if flags.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: image name required.\n\n")
		flags.Usage()
		os.Exit(1)
	}

	// -d implies no -i and no -t (user can still specify but we clamp it)
	if *detach {
		*interactive = false
		*tty = false
	}

	registry, namespace, repo, tag := image.ParseImageTarget(flags.Arg(0))
	fullRef := image.FullRef(registry, namespace, repo, tag)

	opts := container.RunOptions{
		Interactive: *interactive,
		TTY:         *tty,
		Detach:      *detach,
		Name:        *name,
		Hostname:    *hostname,
		Limits: container.CgroupSpecs{
			MemoryMax: *memory,
			CPUMax:    *cpu,
			PidsMax:   *pids,
		},
	}

	userCmd := flags.Args()[1:] // everything after image becomes COMMAND
	if err := container.RunImage(fullRef, userCmd, opts); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}

func pullCommand(args []string) {
	flags := flag.NewFlagSet("pull", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s pull IMAGE\n", os.Args[0])
	}
	flags.Parse(args)
	if flags.NArg() < 1 {
		flags.Usage()
		os.Exit(1)
	}
	imageName := flags.Arg(0)
	if err := image.PullImage(imageName); err != nil {
		log.Fatalf("pull failed: %v", err)
	}
}

func rmCommand(args []string) {
	flags := flag.NewFlagSet("rm", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s rm IMAGE\n", os.Args[0])
	}
	flags.Parse(args)
	if flags.NArg() < 1 {
		flags.Usage()
		os.Exit(1)
	}

	if flags.Arg(0) == "all" {
		containers, err := container.ListContainers()
		if err != nil {
			log.Fatalf("failed to remove all containers: %v\n", err)
		}
		for _, id := range containers {
			if err := container.RemoveContainer(id); err != nil {
				log.Fatalf("remove conatiner failed: %v", err)
			}
		}
		container.CleanupNet()
		fmt.Println("all containers are removed")
		return
	}

	if !isValidHex(flags.Arg(0)) {
		registry, namespace, repo, tag := image.ParseImageTarget(flags.Arg(0))
		fullRef := image.FullRef(registry, namespace, repo, tag)
		if err := image.RemoveImage(fullRef); err != nil {
			log.Fatalf("remove image failed: %v", err)
		}
		fmt.Printf("image %s is removed\n", flags.Arg(0))
	} else {
		if err := container.RemoveContainer(flags.Arg(0)); err != nil {
			log.Fatalf("remove conatiner failed: %v", err)
		}
		fmt.Printf("conatiner with ID %s is removed\n", flags.Arg(0))
	}
}

func verifyCommand(args []string) {
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s verify IMAGE\n", os.Args[0])
	}
	flags.Parse(args)
	if flags.NArg() < 1 {
		flags.Usage()
		os.Exit(1)
	}
	registry, namespace, repo, tag := image.ParseImageTarget(flags.Arg(0))
	fullRef := image.FullRef(registry, namespace, repo, tag)
	err := image.VerifyImage(fullRef)
	if err != nil {
		if strings.HasPrefix(err.Error(), "image corrupted:") {
			fmt.Printf("Critical: %v\n", err)
			fmt.Printf("Run '%s rm %s' to remove it, then re-download.\n",
				os.Args[0], flags.Arg(0))
			os.Exit(1)
		} else {
			log.Fatalf("Failed to complete verification: %v\n", err)
		}
	}
	fmt.Println("Image is fine")
}

func imagesCommand(args []string) {
	if len(args) != 0 && args[0] == "--help" {
		fmt.Fprintf(os.Stderr, "Usage: %s images\n", os.Args[0])
		return
	}
	images, err := image.ListImages()
	if err != nil {
		log.Fatalf("failed to list images: %v\n", err)
	}
	if len(images) == 0 {
		fmt.Println("No images found.")
		return
	}

	fmt.Printf("%-35s %-10s\n", "IMAGE", "DISK USAGE")
	for _, ref := range images {
		size, err := image.ImageSize(ref)
		if err != nil || size == 0 {
			fmt.Printf("%-45s %-10s\n", ref, "unknown")
			continue
		}
		fmt.Printf("%-45s %-10s\n", ref, HumanSize(size))
	}
}

func containersCommand(args []string) {
	if len(args) != 0 && args[0] == "--help" {
		fmt.Fprintf(os.Stderr, "Usage: %s containers\n", os.Args[0])
		return
	}
	containers, err := container.ListContainers()
	if err != nil {
		log.Fatalf("failed to list containers: %v\n", err)
	}
	if len(containers) == 0 {
		fmt.Println("No containers found.")
		return
	}

	fmt.Printf("%-15s %-35s %-15s %-12s %s\n",
		"CONTAINER ID", "IMAGE", "COMMAND", "STATUS", "CREATED")

	for _, id := range containers {
		path := image.ContainerPath(id)
		info, err := os.Stat(path)
		if err != nil {
			log.Printf("skipping container %s: %v", id, err)
			continue
		}

		imageNameBytes, err := os.ReadFile(filepath.Join(path, "image"))
		if err != nil {
			log.Printf("skipping container %s: failed to read image name: %v", id, err)
			continue
		}
		imageName := strings.TrimSpace(string(imageNameBytes))

		commandStr := readContainerCommand(path)

		status := "Up"
		if image.PathExists(filepath.Join(path, "exit-code")) {
			exitCodeBytes, err := os.ReadFile(filepath.Join(path, "exit-code"))
			if err == nil {
				status = fmt.Sprintf("Exited (%s)", strings.TrimSpace(string(exitCodeBytes)))
			} else {
				status = "Exited"
			}
		}

		createdAgo := time.Since(info.ModTime()).Truncate(time.Second).String() + " ago"

		shortID := id
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		fmt.Printf("%-15s %-35s %-15s %-12s %s\n",
			shortID, imageName, commandStr, status, createdAgo)
	}
}

func readContainerCommand(containerPath string) string {
	configBytes, err := os.ReadFile(filepath.Join(containerPath, "config.json"))
	if err != nil {
		log.Printf("warning: could not read config.json for container %s: %v", containerPath, err)
		return "?"
	}
	var cc container.ContainerConfig
	if err := json.Unmarshal(configBytes, &cc); err != nil {
		log.Printf("warning: malformed config.json for container %s: %v", containerPath, err)
		return "?"
	}
	if len(cc.Cmd) == 0 {
		return "(no command)"
	}
	cmd := strings.Join(cc.Cmd, " ")
	if len(cmd) > 12 {
		cmd = cmd[:12] + "…"
	}
	return cmd
}

func gcCommand(args []string) {
	if len(args) != 0 && args[0] == "--help" {
		fmt.Fprintf(os.Stderr, "Usage: %s gc\n", os.Args[0])
		return
	}
	if err := image.GarbageCollect(); err != nil {
		log.Fatalf("garbage collection failed: %v\n", err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: orca <command> [args...]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  run        Run a command in a new container\n")
	fmt.Fprintf(os.Stderr, "  pull       Pull an image from a registry\n")
	fmt.Fprintf(os.Stderr, "  rm         Remove an image tag or container\n")
	fmt.Fprintf(os.Stderr, "  verify     Verify structural integrity of an image\n")
	fmt.Fprintf(os.Stderr, "  images     List downloaded image tags\n")
	fmt.Fprintf(os.Stderr, "  containers List containers\n")
	fmt.Fprintf(os.Stderr, "  gc         Run garbage collection on unused blobs/layers\n")
	fmt.Fprintf(os.Stderr, "\nRun 'orca <command> --help' for more information on a command.\n")
}

func HumanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func isValidHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
