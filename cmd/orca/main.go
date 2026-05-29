package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

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

	switch os.Args[1] {
	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		runCmd.Parse(os.Args[2:])
		args := runCmd.Args()
		if len(args) < 1 {
			log.Fatalf("Usage: %s run <image> [command...]\n", os.Args[0])
		}
		if err := container.RunImage(args[0], args[1:]); err != nil {
			log.Fatalf("run failed: %v", err)
		}
	case "_init_":
		if err := container.Init(); err != nil {
			log.Fatalf("run failed: %v", err)
		}
	case "pull":
		pullCmd := flag.NewFlagSet("pull", flag.ExitOnError)
		pullCmd.Parse(os.Args[2:])
		if pullCmd.NArg() < 1 {
			log.Fatalf("Usage: %s pull <image>\n", os.Args[0])
		}
		if err := image.PullImage(pullCmd.Arg(0)); err != nil {
			log.Fatalf("pull failed: %v", err)
		}

	case "rm":
		rmCmd := flag.NewFlagSet("rm", flag.ExitOnError)
		rmCmd.Parse(os.Args[2:])
		if rmCmd.NArg() < 1 {
			log.Fatalf("Usage: %s rm <image:tag>\n", os.Args[0])
		}
		if err := image.RemoveImage(rmCmd.Arg(0)); err != nil {
			log.Fatalf("remove failed: %v", err)
		}

	case "verify":
		verifyCmd := flag.NewFlagSet("verify", flag.ExitOnError)
		verifyCmd.Parse(os.Args[2:])
		if verifyCmd.NArg() < 1 {
			log.Fatalf("Usage: %s verify <image:tag>\n", os.Args[0])
		}
		err := image.VerifyImage(verifyCmd.Arg(0))
		if err != nil {
			if strings.HasPrefix(err.Error(), "image corrupted:") {
				fmt.Printf("Critical: %v\n", err)
				fmt.Printf("Run '%s rm %s' to remove it, then re-download.\n",
					os.Args[0], verifyCmd.Arg(0))
				os.Exit(1)
			} else {
				log.Fatalf("Failed to complete verification: %v\n", err)
			}
		}
		fmt.Println("Image is fine")

	case "images":
		images, err := image.ListImages()
		if err != nil {
			log.Fatalf("failed to list images: %v\n", err)
		}
		for _, img := range images {
			fmt.Println(img)
		}

	case "gc":
		if err := image.GarbageCollect(); err != nil {
			log.Fatalf("garbage collection failed: %v\n", err)
		}

	default:
		fmt.Fprintf(os.Stderr, "no such command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: orca <command> [args...]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  run     Run a command in a new container\n")
	fmt.Fprintf(os.Stderr, "  pull    Pull an image from registry\n")
	fmt.Fprintf(os.Stderr, "  rm      Remove an image tag\n")
	fmt.Fprintf(os.Stderr, "  verify  Verify structural integrity of an image\n")
	fmt.Fprintf(os.Stderr, "  images  List downloaded image tags\n")
	fmt.Fprintf(os.Stderr, "  gc      Run garbage collection on unused blobs/layers\n")
}
