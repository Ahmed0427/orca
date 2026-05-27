package image

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ahmed0427/orca/pkg/progress"
)

func PullImage(target string) error {
	namespace, repo, tag := parseImageTarget(target)

	client, err := NewClient(namespace, repo)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	manifestResult, err := client.GetManifest(tag)
	if err != nil {
		return fmt.Errorf("failed to get manifest: %w", err)
	}

	manifestPath := tagPath(fmt.Sprintf("%s:%s", repo, tag))
	if err := os.WriteFile(manifestPath, manifestResult.RawBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	confDigest := manifestResult.Manifest.Config.Digest
	conf, confBytes, err := client.GetConfig(confDigest, manifestResult.Manifest.Config.Size)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	if err := os.WriteFile(blobPath(confDigest), confBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config blob: %w", err)
	}

	// Progress layout management (Assumes MultiProgress is in a progress pkg or here)
	pre := []string{fmt.Sprintf("Pulling from %s/%s with tag=%s", namespace, repo, tag)}
	post := []string{
		fmt.Sprintf("Digest: %s", manifestResult.Digest),
		fmt.Sprintf("Status: Downloaded newer image for %s:%s", repo, tag),
	}
	mp := progress.NewMultiProgress(pre, post)

	for _, l := range manifestResult.Manifest.Layers {
		if !blobExists(l.Digest) {
			title := strings.TrimPrefix(l.Digest, "sha256:")[:12]
			mp.AddTask(title, l.Digest, l.Size)
		}
	}
	mp.Render()

	for _, task := range mp.Tasks {
		err := func() error {
			dst, err := os.Create(blobPath(task.ID))
			if err != nil {
				return fmt.Errorf("failed to create layer file: %w", err)
			}
			defer dst.Close()

			proxy := progress.ProgressProxy{Task: task, Layout: mp, Writer: dst}
			if err := client.DownloadLayer(task.ID, task.Total, proxy); err != nil {
				return fmt.Errorf("failed to download layer %s: %w", task.ID, err)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(manifestResult.Manifest.Layers))

	fmt.Print("Extracting layers... ")

	for i, l := range manifestResult.Manifest.Layers {
		wg.Add(1)
		go func(index int, layerDigest string) {
			defer wg.Done()

			id := layerID(conf.Rootfs.DiffIds[index])
			if layerExists(id) {
				return
			}

			f, err := os.Open(blobPath(layerDigest))
			if err != nil {
				errChan <- err
				return
			}
			defer f.Close()

			if err := ExtractLayerTGZ(f, layerPath(id)); err != nil {
				errChan <- err
				return
			}
			_ = os.Remove(blobPath(layerDigest)) // cleanup compressed blob right after extraction
		}(i, l.Digest)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			return fmt.Errorf("extraction error: %w", err)
		}
	}

	fmt.Println("Done")
	return nil
}
