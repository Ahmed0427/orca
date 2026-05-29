package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRegistryBase = "https://registry-1.docker.io/v2/"
	defaultAuthService  = "https://auth.docker.io/token"
	defaultNamespace    = "library"
	defaultTag          = "latest"
)

var (
	ErrNotFoundOrPrivate = errors.New("image not found or is private")
)

type AuthResponse struct {
	Token string `json:"token"`
}

type ManifestResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`

	Config struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"config"`

	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`

	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

type ConfigBlob struct {
	Architecture string `json:"architecture"`
	Config       struct {
		Cmd        []string `json:"Cmd"`
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"config"`
	Created time.Time `json:"created"`
	History []struct {
		Comment    string    `json:"comment"`
		Created    time.Time `json:"created"`
		CreatedBy  string    `json:"created_by"`
		EmptyLayer bool      `json:"empty_layer,omitempty"`
	} `json:"history"`
	Os     string `json:"os"`
	Rootfs struct {
		DiffIds []string `json:"diff_ids"`
		Type    string   `json:"type"`
	} `json:"rootfs"`
}

type Client struct {
	httpClient *http.Client
	registry   string
	token      string
	namespace  string
	repo       string
}

func NewClient(namespace, repo string) (*Client, error) {
	c := &Client{
		httpClient: &http.Client{},
		registry:   defaultRegistryBase,
		namespace:  namespace,
		repo:       repo,
	}

	if err := c.Authenticate(); err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	return c, nil
}

func ParseImageTarget(targetImage string) (string, string, string) {
	var namespace, repo, tag string

	colonIndex := strings.LastIndex(targetImage, ":")
	if colonIndex != -1 {
		tag = targetImage[colonIndex+1:]
		targetImage = targetImage[:colonIndex]
	} else {
		tag = defaultTag
	}

	slashIndex := strings.Index(targetImage, "/")
	if slashIndex != -1 {
		namespace = targetImage[:slashIndex]
		repo = targetImage[slashIndex+1:]
	} else {
		namespace = defaultNamespace
		repo = targetImage
	}

	return namespace, repo, tag
}

func (c *Client) Authenticate() error {
	url := fmt.Sprintf("%s?service=registry.docker.io&scope=repository:%s:pull",
		defaultAuthService, fmt.Sprintf("%s/%s", c.namespace, c.repo))

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth service returned status: %d", resp.StatusCode)
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return err
	}

	c.token = authResp.Token
	return nil
}

func (c *Client) NewRequest(method, endpoint string) (*http.Request, error) {
	req, err := http.NewRequest(method, c.registry+c.namespace+"/"+c.repo+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

type ManifestResult struct {
	Manifest *ManifestResponse
	RawBytes []byte
	Digest   string
}

// GetManifest retrieves the manifest for a tag or digest. Auto-resolves multi-arch.
func (c *Client) GetManifest(reference string) (*ManifestResult, error) {
	req, err := c.NewRequest("GET", "/manifests/"+reference)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return nil, ErrNotFoundOrPrivate
		default:
			return nil, fmt.Errorf("registry returned status: %d", resp.StatusCode)
		}
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var manifest ManifestResponse
	if err := json.Unmarshal(bodyBytes, &manifest); err != nil {
		return nil, err
	}

	// handle manifest lists (multi-arch indexes)
	if len(manifest.Manifests) > 0 {
		for _, m := range manifest.Manifests {
			if m.Platform.Architecture == runtime.GOARCH && m.Platform.OS == runtime.GOOS {
				// recursively call for the specific platform digest
				return c.GetManifest(m.Digest)
			}
		}
		return nil, fmt.Errorf("could not find image version for OS:%s ARCH:%s",
			runtime.GOOS, runtime.GOARCH)
	}

	hash := sha256.New()
	hash.Write(bodyBytes)
	digestHex := fmt.Sprintf("sha256:%s", hex.EncodeToString(hash.Sum(nil)))

	if strings.HasPrefix(reference, "sha256:") && digestHex != reference {
		return nil, fmt.Errorf("manifest digest doesn't match")
	}

	return &ManifestResult{
		Manifest: &manifest,
		RawBytes: bodyBytes,
		Digest:   digestHex,
	}, nil
}

// GetConfig fetches the JSON image configuration.
func (c *Client) GetConfig(digest string, size int64) (*ConfigBlob, []byte, error) {
	req, err := c.NewRequest("GET", "/blobs/"+digest)
	if err != nil {
		return nil, nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("failed to fetch config blob, status: %d", resp.StatusCode)
	}

	buf, err := io.ReadAll(io.LimitReader(resp.Body, size))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if int64(len(buf)) != size {
		return nil, nil, fmt.Errorf("size mismatch: expected %d bytes, got %d", size, len(buf))
	}

	hash := sha256.New()
	hash.Write(buf)
	db := hash.Sum(nil)
	d := hex.EncodeToString(db)
	if fmt.Sprintf("sha256:%s", d) != digest {
		return nil, nil, fmt.Errorf("config digest don't match")
	}

	var conf ConfigBlob
	if err := json.Unmarshal(buf, &conf); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal json: %w", err)
	}

	return &conf, buf, nil
}

// DownloadLayer streams a compressed layer blob to the provided io.Writer.
func (c *Client) DownloadLayer(digest string, expectedSize int64, dest io.Writer) error {
	req, err := c.NewRequest("GET", "/blobs/"+digest)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch layer blob, status: %d", resp.StatusCode)
	}

	hash := sha256.New()

	written, err := io.Copy(io.MultiWriter(dest, hash), resp.Body)
	if err != nil {
		return err
	}

	if expectedSize > 0 && written != expectedSize {
		return fmt.Errorf("size mismatch for %s: expected %d, got %d",
			digest, expectedSize, written)
	}

	sha256Bytes := hash.Sum(nil)
	calculatedDigest := hex.EncodeToString(sha256Bytes)

	if strings.TrimPrefix(digest, "sha256:") != calculatedDigest {
		return fmt.Errorf("layer digest doesn't match")
	}

	return nil
}
