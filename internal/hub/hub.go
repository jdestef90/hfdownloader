package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const DefaultBaseURL = "https://huggingface.co"

// ParsedURL represents a parsed Hugging Face Hub repository reference.
type ParsedURL struct {
	Repo     string
	Revision string
}

// SidecarKind identifies the type of companion GGUF Sidecar.
type SidecarKind string

const (
	SidecarMMProj SidecarKind = "mmproj"
	SidecarMTP    SidecarKind = "mtp"
)

// Sidecar describes a companion mmproj or mtp GGUF file in a Hub repository.
type Sidecar struct {
	Path   string
	Size   int64
	SHA256 string
	Kind   SidecarKind
}

// Shard describes a shard's position within a multi-file Quant.
type Shard struct {
	Index int
	Total int
}

// File describes a single GGUF file in a Hub repository.
type File struct {
	Path   string
	Size   int64
	SHA256 string
	Shard  *Shard
}

// Quant describes a selectable GGUF quantization (single file or grouped shards).
type Quant struct {
	Label         string
	Files         []File
	Size          int64
	Sidecars      []Sidecar
	ContextLength int
	Architecture  string
}

// RepoInfo holds the discovered Quants, Sidecars, and metadata for a Hub repository.
type RepoInfo struct {
	Repo          string
	Revision      string
	Quants        []Quant
	Sidecars      []Sidecar
	ContextLength int
	Architecture  string
}

// AuthError indicates that the Hub refused access (HTTP 401 or 403), e.g. for a gated repository.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return e.Message
}

// ClientOptions configures a Hub API client.
type ClientOptions struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// Client queries the Hugging Face Hub API.
type Client struct {
	BaseURL    string
	Token      string
	httpClient *http.Client
}

// NewClient creates a Hub API client.
func NewClient(opts ClientOptions) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		BaseURL:    base,
		Token:      strings.TrimSpace(opts.Token),
		httpClient: httpClient,
	}
}

var (
	shardPattern   = regexp.MustCompile(`(?i)^(.*)-(\d{5})-of-(\d{5})\.gguf$`)
	quantPattern   = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD[-_])?(?:IQ\d+(?:_[A-Z0-9]+)*|Q\d+(?:_[A-Z0-9]+)*|BF16|F16|F32|FP16))(?:[-_.]|$)`)
	sha256Pattern  = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)
	mmprojPattern  = regexp.MustCompile(`(?i)(^|[-_.])mmproj([-_.]|$)`)
	mtpPattern     = regexp.MustCompile(`(?i)(^|[-_.])mtp([-_.]|$)`)
	linkNextRegexp = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)
)

func isSHA256(s string) bool {
	return sha256Pattern.MatchString(s)
}

// ParseHubURL parses a Hugging Face repository URL or "org/repo" shorthand.
func ParseHubURL(input string) (ParsedURL, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ParsedURL{}, errors.New("enter a Hugging Face repository URL")
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return ParsedURL{}, fmt.Errorf("not a valid URL: %s", trimmed)
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "huggingface.co" && host != "www.huggingface.co" {
			return ParsedURL{}, errors.New("only huggingface.co URLs are supported")
		}
		parts := splitNonEmpty(parsed.Path)
		if len(parts) < 2 {
			return ParsedURL{}, errors.New("the URL must contain an organization and a repository name")
		}
		repo := parts[0] + "/" + parts[1]
		revision := "main"
		if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
			if dec, err := url.PathUnescape(parts[3]); err == nil && dec != "" {
				revision = dec
			}
		}
		return ParsedURL{Repo: repo, Revision: revision}, nil
	}

	repo := strings.Trim(trimmed, "/")
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ParsedURL{}, errors.New("enter a URL like https://huggingface.co/org/repo")
	}
	return ParsedURL{Repo: repo, Revision: "main"}, nil
}

func splitNonEmpty(p string) []string {
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func encodePathSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

// ResolveURL constructs the direct download URL for a file in a Hub repository.
func (c *Client) ResolveURL(repo, filePath, revision string) string {
	if revision == "" {
		revision = "main"
	}
	return fmt.Sprintf(
		"%s/%s/resolve/%s/%s?download=true",
		c.BaseURL,
		encodePathSegments(repo),
		url.PathEscape(revision),
		encodePathSegments(filePath),
	)
}

type treeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
	GGUF *struct {
		ContextLength int    `json:"context_length"`
		Architecture  string `json:"architecture"`
	} `json:"gguf"`
}

type modelInfo struct {
	GGUF *struct {
		ContextLength int    `json:"context_length"`
		Architecture  string `json:"architecture"`
	} `json:"gguf"`
}

// ListQuants fetches the repository tree and metadata from the Hub and groups GGUF Quants and Sidecars.
func (c *Client) ListQuants(ctx context.Context, repo, revision string) (RepoInfo, error) {
	if revision == "" {
		revision = "main"
	}

	entries, err := c.fetchTree(ctx, repo, revision)
	if err != nil {
		return RepoInfo{}, err
	}
	info, err := c.fetchModelInfo(ctx, repo)
	if err != nil {
		return RepoInfo{}, err
	}
	return buildRepoInfo(repo, revision, entries, info), nil
}

// HeadSHA256 checks x-linked-etag or etag via HEAD for a file's SHA-256 digest.
func (c *Client) HeadSHA256(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		return "", err
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &AuthError{
			Message: "the Hub refused access to this repository; set hubToken (HFDOWNLOADER_HUB_TOKEN) for gated or private repositories",
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	tag := resp.Header.Get("X-Linked-Etag")
	if tag == "" {
		tag = resp.Header.Get("ETag")
	}
	tag = strings.TrimPrefix(tag, "W/")
	tag = strings.Trim(tag, `"`)
	if isSHA256(tag) {
		return strings.ToLower(tag), nil
	}
	return "", nil
}

// AuthHeaders returns HTTP headers including optional Bearer auth for downloading from the Hub.
func (c *Client) AuthHeaders() http.Header {
	h := make(http.Header)
	h.Set("User-Agent", "hfdownloader/0.1")
	if c.Token != "" {
		h.Set("Authorization", "Bearer "+c.Token)
	}
	return h
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hfdownloader/0.1")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func (c *Client) fetchTree(ctx context.Context, repo, revision string) ([]treeEntry, error) {
	nextURL := fmt.Sprintf(
		"%s/api/models/%s/tree/%s?recursive=true",
		c.BaseURL,
		encodePathSegments(repo),
		url.PathEscape(revision),
	)

	var all []treeEntry
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		c.applyHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to contact the Hub: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return nil, &AuthError{
				Message: "the Hub refused access to this repository; set hubToken (HFDOWNLOADER_HUB_TOKEN) to browse gated or private repositories",
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("the Hub returned HTTP %d for %s; check the repository URL", resp.StatusCode, repo)
		}

		var page []treeEntry
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected Hub response while listing %s: %w", repo, err)
		}
		linkHeader := resp.Header.Get("Link")
		resp.Body.Close()

		all = append(all, page...)
		nextURL = resolveNextLink(c.BaseURL, linkHeader)
	}
	return all, nil
}

func (c *Client) fetchModelInfo(ctx context.Context, repo string) (*modelInfo, error) {
	endpoint := fmt.Sprintf("%s/api/models/%s?blobs=true", c.BaseURL, encodePathSegments(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &AuthError{
			Message: "the Hub refused access to this repository; set hubToken (HFDOWNLOADER_HUB_TOKEN) to browse gated or private repositories",
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	var info modelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, nil
	}
	return &info, nil
}

func resolveNextLink(baseURL, header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		m := linkNextRegexp.FindStringSubmatch(part)
		if len(m) < 2 {
			continue
		}
		ref, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		base, err := url.Parse(baseURL)
		if err != nil {
			continue
		}
		return base.ResolveReference(ref).String()
	}
	return ""
}

func sidecarKind(filePath string) (SidecarKind, bool) {
	base := path.Base(filePath)
	if mmprojPattern.MatchString(base) {
		return SidecarMMProj, true
	}
	if mtpPattern.MatchString(base) {
		return SidecarMTP, true
	}
	return "", false
}

func quantLabelFromStem(stem string) string {
	m := quantPattern.FindStringSubmatch(stem)
	if len(m) >= 2 && m[1] != "" {
		return strings.ToUpper(m[1])
	}
	return stem
}

func shaFromEntry(e treeEntry) string {
	if e.LFS != nil && isSHA256(e.LFS.OID) {
		return strings.ToLower(e.LFS.OID)
	}
	if isSHA256(e.OID) {
		return strings.ToLower(e.OID)
	}
	return ""
}

func buildRepoInfo(repo, revision string, entries []treeEntry, info *modelInfo) RepoInfo {
	var repoContext int
	var repoArch string
	if info != nil && info.GGUF != nil {
		repoContext = info.GGUF.ContextLength
		repoArch = info.GGUF.Architecture
	}

	type quantGroup struct {
		label         string
		files         []File
		contextLength int
		architecture  string
	}

	var sidecars []Sidecar
	groups := make(map[string]*quantGroup)
	var groupOrder []string

	for _, entry := range entries {
		if entry.Type != "file" || entry.Path == "" {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Path), ".gguf") {
			continue
		}

		size := entry.Size
		if size == 0 && entry.LFS != nil {
			size = entry.LFS.Size
		}
		sha := shaFromEntry(entry)

		if kind, ok := sidecarKind(entry.Path); ok {
			sidecars = append(sidecars, Sidecar{
				Path:   entry.Path,
				Size:   size,
				SHA256: sha,
				Kind:   kind,
			})
			continue
		}

		dir := path.Dir(entry.Path)
		base := path.Base(entry.Path)
		shardMatch := shardPattern.FindStringSubmatch(base)

		var stem, key string
		var shard *Shard
		if len(shardMatch) == 4 {
			stem = shardMatch[1]
			key = "shard:" + dir + "/" + stem
			idx, _ := strconv.Atoi(shardMatch[2])
			total, _ := strconv.Atoi(shardMatch[3])
			shard = &Shard{Index: idx, Total: total}
		} else {
			stem = base[:len(base)-len(".gguf")]
			key = "file:" + dir + "/" + stem
		}

		group, exists := groups[key]
		if !exists {
			ctxLen := repoContext
			arch := repoArch
			if entry.GGUF != nil {
				if entry.GGUF.ContextLength > 0 {
					ctxLen = entry.GGUF.ContextLength
				}
				if entry.GGUF.Architecture != "" {
					arch = entry.GGUF.Architecture
				}
			}
			group = &quantGroup{
				label:         quantLabelFromStem(stem),
				contextLength: ctxLen,
				architecture:  arch,
			}
			groups[key] = group
			groupOrder = append(groupOrder, key)
		}

		group.files = append(group.files, File{
			Path:   entry.Path,
			Size:   size,
			SHA256: sha,
			Shard:  shard,
		})
	}

	sort.Slice(sidecars, func(i, j int) bool {
		if sidecars[i].Kind != sidecars[j].Kind {
			return sidecars[i].Kind < sidecars[j].Kind
		}
		return sidecars[i].Path < sidecars[j].Path
	})

	quants := make([]Quant, 0, len(groupOrder))
	for _, key := range groupOrder {
		g := groups[key]
		sort.Slice(g.files, func(i, j int) bool {
			ai, aj := 0, 0
			if g.files[i].Shard != nil {
				ai = g.files[i].Shard.Index
			}
			if g.files[j].Shard != nil {
				aj = g.files[j].Shard.Index
			}
			if ai != aj {
				return ai < aj
			}
			return g.files[i].Path < g.files[j].Path
		})

		var totalSize int64
		for _, f := range g.files {
			totalSize += f.Size
		}

		copiedSidecars := make([]Sidecar, len(sidecars))
		copy(copiedSidecars, sidecars)

		quants = append(quants, Quant{
			Label:         g.label,
			Files:         g.files,
			Size:          totalSize,
			Sidecars:      copiedSidecars,
			ContextLength: g.contextLength,
			Architecture:  g.architecture,
		})
	}

	sort.Slice(quants, func(i, j int) bool {
		if quants[i].Size != quants[j].Size {
			return quants[i].Size < quants[j].Size
		}
		return quants[i].Label < quants[j].Label
	})

	return RepoInfo{
		Repo:          repo,
		Revision:      revision,
		Quants:        quants,
		Sidecars:      sidecars,
		ContextLength: repoContext,
		Architecture:  repoArch,
	}
}

// FormatBytes formats a byte count into a human-readable IEC unit string.
func FormatBytes(bytes int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
		tib = 1024 * gib
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.2f TiB", float64(bytes)/float64(tib))
	case bytes >= gib:
		return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(gib))
	case bytes >= mib:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(mib))
	case bytes >= kib:
		return fmt.Sprintf("%.1f KiB", float64(bytes)/float64(kib))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
