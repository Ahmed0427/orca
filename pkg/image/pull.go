package image

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ahmed0427/orca/pkg/progress"
)

func PullImage(target string) error {
	namespace, repo, tag := ParseImageTarget(target)

	client, err := NewClient(namespace, repo)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	manifestResult, err := client.GetManifest(tag)
	if err != nil {
		return fmt.Errorf("failed to get manifest: %w", err)
	}

	manifestPath := TagPath(fmt.Sprintf("%s:%s", repo, tag))
	if err := os.WriteFile(manifestPath, manifestResult.RawBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	confDigest := manifestResult.Manifest.Config.Digest
	conf, confBytes, err := client.GetConfig(confDigest, manifestResult.Manifest.Config.Size)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	if err := os.WriteFile(BlobPath(confDigest), confBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config blob: %w", err)
	}

	post := []string{
		fmt.Sprintf("Digest: %s", manifestResult.Digest),
		fmt.Sprintf("Status: Downloaded newer image for %s:%s", repo, tag),
	}
	pre := []string{fmt.Sprintf("Pulling from %s/%s with tag=%s", namespace, repo, tag)}
	mp := progress.NewMultiProgress(pre, post)

	for _, l := range manifestResult.Manifest.Layers {
		if !BlobExists(l.Digest) {
			title := strings.TrimPrefix(l.Digest, "sha256:")[:12]
			mp.AddTask(title, l.Digest, l.Size)
		}
	}
	mp.Render()

	for _, task := range mp.Tasks {
		err := func() error {
			dst, err := os.Create(BlobPath(task.ID))
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

			id := LayerID(conf.Rootfs.DiffIds[index])
			if LayerExists(id) {
				return
			}

			f, err := os.Open(BlobPath(layerDigest))
			if err != nil {
				errChan <- err
				return
			}
			defer f.Close()

			if err := ExtractLayerTGZ(f, LayerPath(id)); err != nil {
				errChan <- err
				return
			}
			_ = os.Remove(BlobPath(layerDigest)) // cleanup compressed blob right after extraction
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
