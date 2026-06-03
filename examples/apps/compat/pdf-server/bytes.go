package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PDF byte-range fetcher backing the read_pdf_bytes tool. Supports two
// schemes:
//
//   - file:// and bare local paths — os.File + Seek/ReadAt
//   - http:// and https://       — HTTP Range header
//
// Each call returns at most maxChunkBytes (512KB). Upstream caches
// per-URL byte data and totalBytes; for the test runs we target the
// rangeServer fixtures sit on loopback, so cache pressure is irrelevant
// and skipping the cache keeps the implementation minimal.

// rangeResult is the wire shape we hand back to read_pdf_bytes.
type rangeResult struct {
	URL        string `json:"url"`
	Bytes      string `json:"bytes"`      // base64-encoded chunk
	Offset     int    `json:"offset"`
	ByteCount  int    `json:"byteCount"`
	TotalBytes int    `json:"totalBytes"`
	HasMore    bool   `json:"hasMore"`
}

// fetchPDFRange dispatches by URL scheme. The chunk is clamped to
// maxChunkBytes by the input schema's `maximum`, but we re-clamp here
// in case the underlying transport returns more (HTTP servers occasionally
// honor partial Range requests with more bytes than asked).
func fetchPDFRange(ctx context.Context, rawURL string, offset, byteCount int) (rangeResult, error) {
	if byteCount <= 0 {
		byteCount = maxChunkBytes
	}
	if byteCount > maxChunkBytes {
		byteCount = maxChunkBytes
	}

	scheme, localPath, isLocal := normalizeURL(rawURL)
	if isLocal {
		return readLocalRange(rawURL, localPath, offset, byteCount)
	}
	switch scheme {
	case "http", "https":
		return readHTTPRange(ctx, rawURL, offset, byteCount)
	default:
		return rangeResult{}, fmt.Errorf("unsupported URL scheme %q", scheme)
	}
}

// normalizeURL classifies the URL and surfaces the local-disk path
// when applicable. Returns scheme + (for local) the filesystem path.
func normalizeURL(rawURL string) (scheme, localPath string, isLocal bool) {
	if strings.HasPrefix(rawURL, "file://") {
		// file:// always has the file:// prefix; strip and decode.
		u, err := url.Parse(rawURL)
		if err == nil {
			path := u.Path
			if path == "" {
				path = strings.TrimPrefix(rawURL, "file://")
			}
			return "file", path, true
		}
	}
	if strings.HasPrefix(rawURL, "/") || filepath.VolumeName(rawURL) != "" {
		return "file", rawURL, true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false
	}
	return strings.ToLower(u.Scheme), "", false
}

func readLocalRange(rawURL, path string, offset, byteCount int) (rangeResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return rangeResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return rangeResult{}, fmt.Errorf("stat %s: %w", path, err)
	}
	total := int(stat.Size())
	if offset >= total {
		return rangeResult{URL: rawURL, Bytes: "", Offset: offset, ByteCount: 0, TotalBytes: total, HasMore: false}, nil
	}
	end := offset + byteCount
	if end > total {
		end = total
	}
	buf := make([]byte, end-offset)
	if _, err := f.ReadAt(buf, int64(offset)); err != nil && err != io.EOF {
		return rangeResult{}, fmt.Errorf("read %s: %w", path, err)
	}
	return rangeResult{
		URL:        rawURL,
		Bytes:      base64.StdEncoding.EncodeToString(buf),
		Offset:     offset,
		ByteCount:  len(buf),
		TotalBytes: total,
		HasMore:    end < total,
	}, nil
}

// httpClient pins a short request timeout so a slow upstream can't
// block the long-poll viewer for the full default 0-second-no-deadline.
var httpClient = &http.Client{Timeout: 60 * time.Second}

func readHTTPRange(ctx context.Context, rawURL string, offset, byteCount int) (rangeResult, error) {
	end := offset + byteCount - 1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return rangeResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	resp, err := httpClient.Do(req)
	if err != nil {
		return rangeResult{}, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return rangeResult{}, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(byteCount)))
	if err != nil {
		return rangeResult{}, fmt.Errorf("read body: %w", err)
	}

	// Total size — prefer Content-Range's `*/total`, fall back to
	// Content-Length when the server didn't honor Range (returned 200 OK
	// with the full body). The clamping above keeps us under maxChunkBytes
	// even when the server ignored Range.
	total := offset + len(data)
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 && i+1 < len(cr) {
			if n, err := strconv.Atoi(cr[i+1:]); err == nil {
				total = n
			}
		}
	} else if cl := resp.Header.Get("Content-Length"); cl != "" && resp.StatusCode == http.StatusOK {
		if n, err := strconv.Atoi(cl); err == nil {
			total = n
		}
	}

	return rangeResult{
		URL:        rawURL,
		Bytes:      base64.StdEncoding.EncodeToString(data),
		Offset:     offset,
		ByteCount:  len(data),
		TotalBytes: total,
		HasMore:    offset+len(data) < total,
	}, nil
}

// probeTotalBytes does a 1-byte GET to learn the total file size, used
// by display_pdf to populate the result envelope before any viewer
// streaming kicks in. Returns 0 if the source isn't reachable — the
// viewer treats 0 as "unknown" and the iframe loads via read_pdf_bytes
// anyway.
func probeTotalBytes(ctx context.Context, rawURL string) int {
	res, err := fetchPDFRange(ctx, rawURL, 0, 1)
	if err != nil {
		return 0
	}
	return res.TotalBytes
}
