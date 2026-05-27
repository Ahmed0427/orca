package main

import (
	"fmt"
	"log"
	"os"
	"strings"

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
	case "pull":
		if len(os.Args) < 3 {
			log.Fatalf("Usage: %s pull <image>\n", os.Args[0])
		}
		if err := image.PullImage(os.Args[2]); err != nil {
			log.Fatalf("pull failed: %v", err)
		}

	case "rm":
		if len(os.Args) < 3 {
			log.Fatalf("Usage: %s rm <image:tag>\n", os.Args[0])
		}
		if err := image.RemoveImage(os.Args[2]); err != nil {
			log.Fatalf("remove failed: %v", err)
		}

	case "verify":
		if len(os.Args) < 3 {
			log.Fatalf("Usage: %s verify <image:tag>\n", os.Args[0])
		}
		err := image.VerifyImage(os.Args[2])
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
	fmt.Fprintf(os.Stderr, "  pull    Pull an image from registry\n")
	fmt.Fprintf(os.Stderr, "  rm      Remove an image tag\n")
	fmt.Fprintf(os.Stderr, "  verify  Verify structural integrity of an image\n")
	fmt.Fprintf(os.Stderr, "  images  List downloaded image tags\n")
	fmt.Fprintf(os.Stderr, "  gc      Run garbage collection on unused blobs/layers\n")
}
