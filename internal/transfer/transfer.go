package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jdestef90/hfdownloader/internal/hub"
)

// CommandResult captures the outcome of running a shell command on a Target.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Target abstracts file and command operations on the Router host (SSH or local).
type Target interface {
	Description() string
	Stat(ctx context.Context, targetPath string) (size int64, exists bool, err error)
	WriteFromReader(ctx context.Context, targetPath string, r io.Reader, appendMode bool) error
	Rename(ctx context.Context, from, to string) error
	Remove(ctx context.Context, targetPath string) error
	SHA256(ctx context.Context, targetPath string) (string, error)
	ReadText(ctx context.Context, targetPath string) (content string, exists bool, err error)
	WriteText(ctx context.Context, targetPath string, content string) error
	Run(ctx context.Context, command string) (CommandResult, error)
}

// DownloadFile describes a single file to stream from the Hub into the GGUF Directory.
type DownloadFile struct {
	Path   string
	Size   int64
	SHA256 string
	URL    string
}

// Progress reports live download metrics for a Quant (and optional Sidecars).
type Progress struct {
	File           string
	Transferred    int64
	Total          int64
	SpeedBps       float64
	ETASeconds     float64
	CompletedFiles int
	TotalFiles     int
	Verifying      bool
}

// DownloadedFile records a successfully transferred and verified file on the Target.
type DownloadedFile struct {
	Path string
	Size int64
}

// DownloadResult summarizes a completed transfer.
type DownloadResult struct {
	Files []DownloadedFile
	Bytes int64
}

// DownloadOptions configures DownloadQuant.
type DownloadOptions struct {
	Files      []DownloadFile
	Target     Target
	Directory  string
	Overwrite  bool
	HTTPClient *http.Client
	Headers    http.Header
	OnProgress func(Progress)
	Now        func() time.Time
}

// OverwriteRequiredError is returned when one or more target files already exist and Overwrite is false.
type OverwriteRequiredError struct {
	Paths []string
}

func (e *OverwriteRequiredError) Error() string {
	return fmt.Sprintf("target file(s) already exist and require overwrite confirmation: %s", strings.Join(e.Paths, ", "))
}

// ChecksumError is returned when a downloaded file's SHA-256 does not match the Hub digest.
type ChecksumError struct {
	Path     string
	Expected string
	Actual   string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("SHA-256 mismatch for %s: expected %s, got %s", e.Path, e.Expected, e.Actual)
}

var (
	contentRangeStartRe = regexp.MustCompile(`^bytes (\d+)-`)
	sha256HexRe         = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)
)

// JoinTargetPath joins a directory and filename using POSIX slashes (used identically in ssh and local modes).
func JoinTargetPath(directory, filename string) string {
	trimmed := strings.TrimRight(directory, "/")
	if trimmed == "" {
		return filename
	}
	return trimmed + "/" + filename
}

// DownloadQuant streams files into opts.Directory on opts.Target using their original Hub filenames,
// supporting resume via .part files, progress reporting, cancellation cleanup, and SHA-256 verification.
func DownloadQuant(ctx context.Context, opts DownloadOptions) (DownloadResult, error) {
	if len(opts.Files) == 0 {
		return DownloadResult{}, errors.New("no files selected for download")
	}
	if opts.Target == nil {
		return DownloadResult{}, errors.New("download target is required")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	seen := make(map[string]struct{}, len(opts.Files))
	preexisting := make(map[string]bool)
	var existingPaths []string
	var totalBytes int64

	for _, f := range opts.Files {
		name := path.Base(f.Path)
		targetPath := JoinTargetPath(opts.Directory, name)
		if _, dup := seen[targetPath]; dup {
			return DownloadResult{}, fmt.Errorf("two selected files map to the same target path: %s", targetPath)
		}
		seen[targetPath] = struct{}{}
		totalBytes += f.Size

		_, exists, err := opts.Target.Stat(ctx, targetPath)
		if err != nil {
			return DownloadResult{}, err
		}
		if exists {
			preexisting[targetPath] = true
			existingPaths = append(existingPaths, targetPath)
		}
	}

	if len(existingPaths) > 0 && !opts.Overwrite {
		return DownloadResult{}, &OverwriteRequiredError{Paths: existingPaths}
	}

	baseHeaders := make(http.Header)
	baseHeaders.Set("Accept", "application/octet-stream")
	baseHeaders.Set("Accept-Encoding", "identity")
	baseHeaders.Set("User-Agent", "hfdownloader/0.1")
	for k, vals := range opts.Headers {
		baseHeaders[k] = append([]string(nil), vals...)
	}

	startedAt := now()
	var (
		completedBytes     int64
		sessionBytes       int64
		currentBytes       int64
		completedFiles     int
		currentFile        string
		lastEmit           time.Time
		createdTargetPaths []string
		results            = make([]DownloadedFile, 0, len(opts.Files))
	)

	emit := func(force bool, verifying bool) {
		if opts.OnProgress == nil {
			return
		}
		ts := now()
		if !force && !lastEmit.IsZero() && ts.Sub(lastEmit) < 100*time.Millisecond {
			return
		}
		lastEmit = ts
		transferred := completedBytes + currentBytes
		elapsed := ts.Sub(startedAt).Seconds()
		if elapsed < 0.001 {
			elapsed = 0.001
		}
		speed := float64(sessionBytes) / elapsed
		var eta float64
		if transferred >= totalBytes {
			eta = 0
		} else if speed > 0 {
			eta = float64(totalBytes-transferred) / speed
		} else {
			eta = math.Inf(1)
		}
		opts.OnProgress(Progress{
			File:           currentFile,
			Transferred:    transferred,
			Total:          totalBytes,
			SpeedBps:       speed,
			ETASeconds:     eta,
			CompletedFiles: completedFiles,
			TotalFiles:     len(opts.Files),
			Verifying:      verifying,
		})
	}

	cleanupOnCancel := func(activePart string) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if activePart != "" {
			_ = opts.Target.Remove(cleanupCtx, activePart)
		}
		for _, p := range createdTargetPaths {
			if !preexisting[p] {
				_ = opts.Target.Remove(cleanupCtx, p)
			}
		}
	}

	for _, f := range opts.Files {
		if err := ctx.Err(); err != nil {
			cleanupOnCancel("")
			return DownloadResult{}, err
		}

		name := path.Base(f.Path)
		targetPath := JoinTargetPath(opts.Directory, name)
		partPath := targetPath + ".part"
		currentFile = name
		currentBytes = 0

		expected, err := resolveExpectedSHA(ctx, f, httpClient, baseHeaders)
		if err != nil {
			if ctx.Err() != nil {
				cleanupOnCancel(partPath)
			}
			return DownloadResult{}, err
		}

		partSize, partExists, err := opts.Target.Stat(ctx, partPath)
		if err != nil {
			return DownloadResult{}, err
		}
		if !partExists {
			partSize = 0
		}
		if partSize > f.Size {
			if err := opts.Target.Remove(ctx, partPath); err != nil {
				return DownloadResult{}, err
			}
			partSize = 0
		}

		currentBytes = partSize
		emit(true, false)

		if partSize < f.Size || (f.Size == 0 && !partExists) {
			if err := streamFile(ctx, opts.Target, httpClient, baseHeaders, f, partPath, partSize, func(newCurrentBytes int64, delta int64) {
				currentBytes = newCurrentBytes
				sessionBytes += delta
				emit(false, false)
			}); err != nil {
				if ctx.Err() != nil {
					cleanupOnCancel(partPath)
					return DownloadResult{}, ctx.Err()
				}
				return DownloadResult{}, err
			}
		}

		if err := ctx.Err(); err != nil {
			cleanupOnCancel(partPath)
			return DownloadResult{}, err
		}

		actualSize, _, err := opts.Target.Stat(ctx, partPath)
		if err != nil {
			return DownloadResult{}, err
		}
		if actualSize != f.Size {
			if actualSize > f.Size {
				_ = opts.Target.Remove(ctx, partPath)
			}
			return DownloadResult{}, fmt.Errorf("downloaded %s is %d bytes, expected %d (re-run to resume)", f.Path, actualSize, f.Size)
		}

		if expected != "" {
			emit(true, true)
			actualSHA, err := opts.Target.SHA256(ctx, partPath)
			if err != nil {
				if ctx.Err() != nil {
					cleanupOnCancel(partPath)
					return DownloadResult{}, ctx.Err()
				}
				return DownloadResult{}, err
			}
			if !strings.EqualFold(actualSHA, expected) {
				_ = opts.Target.Remove(context.Background(), partPath)
				return DownloadResult{}, &ChecksumError{
					Path:     targetPath,
					Expected: strings.ToLower(expected),
					Actual:   strings.ToLower(actualSHA),
				}
			}
		}

		if err := opts.Target.Rename(ctx, partPath, targetPath); err != nil {
			if ctx.Err() != nil {
				cleanupOnCancel(partPath)
				return DownloadResult{}, ctx.Err()
			}
			return DownloadResult{}, err
		}

		createdTargetPaths = append(createdTargetPaths, targetPath)
		results = append(results, DownloadedFile{Path: targetPath, Size: f.Size})
		completedBytes += f.Size
		currentBytes = 0
		completedFiles++
		emit(true, false)
	}

	return DownloadResult{
		Files: results,
		Bytes: totalBytes,
	}, nil
}

func streamFile(
	ctx context.Context,
	target Target,
	httpClient *http.Client,
	baseHeaders http.Header,
	f DownloadFile,
	partPath string,
	partSize int64,
	onChunk func(newCurrentBytes int64, delta int64),
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	for k, vals := range baseHeaders {
		req.Header[k] = append([]string(nil), vals...)
	}
	if partSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", partSize))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &hub.AuthError{
			Message: "the Hub refused the download; set hubToken (HFDOWNLOADER_HUB_TOKEN) for gated repositories",
		}
	}

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && partSize == f.Size {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("the Hub returned HTTP %d while downloading %s", resp.StatusCode, f.Path)
	}

	appendMode := false
	resumedBase := int64(0)
	if resp.StatusCode == http.StatusPartialContent && partSize > 0 {
		contentRange := resp.Header.Get("Content-Range")
		if m := contentRangeStartRe.FindStringSubmatch(contentRange); len(m) == 2 {
			start, parseErr := strconv.ParseInt(m[1], 10, 64)
			if parseErr == nil && start == partSize {
				appendMode = true
				resumedBase = partSize
			}
		} else if contentRange == "" {
			appendMode = true
			resumedBase = partSize
		}
	}

	if !appendMode && partSize > 0 {
		if err := target.Remove(ctx, partPath); err != nil {
			return err
		}
		resumedBase = 0
	}

	reader := &countingReader{
		r:       resp.Body,
		current: resumedBase,
		onRead:  onChunk,
	}

	return target.WriteFromReader(ctx, partPath, reader, appendMode)
}

type countingReader struct {
	r       io.Reader
	current int64
	onRead  func(newCurrent int64, delta int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.current += int64(n)
		if c.onRead != nil {
			c.onRead(c.current, int64(n))
		}
	}
	return n, err
}

func resolveExpectedSHA(ctx context.Context, f DownloadFile, httpClient *http.Client, headers http.Header) (string, error) {
	if sha256HexRe.MatchString(f.SHA256) {
		return strings.ToLower(f.SHA256), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, f.URL, nil)
	if err != nil {
		return "", nil
	}
	for k, vals := range headers {
		req.Header[k] = append([]string(nil), vals...)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &hub.AuthError{
			Message: "the Hub refused the download; set hubToken (HFDOWNLOADER_HUB_TOKEN) for gated repositories",
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
	if sha256HexRe.MatchString(tag) {
		return strings.ToLower(tag), nil
	}
	return "", nil
}
