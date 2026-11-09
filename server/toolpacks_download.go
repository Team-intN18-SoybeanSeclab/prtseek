package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errToolpackRangeUnsupported = errors.New("official download server ignored the byte range")

type toolpackHTTPTransport struct{ metadata, assets *http.Transport }

func newToolpackHTTPTransport() *toolpackHTTPTransport {
	metadata := http.DefaultTransport.(*http.Transport).Clone()
	metadata.MaxConnsPerHost, metadata.MaxIdleConnsPerHost = 4, 4
	assets := metadata.Clone()
	assets.Protocols = new(http.Protocols)
	assets.Protocols.SetHTTP1(true)
	assets.ForceAttemptHTTP2 = false
	assets.TLSNextProto = nil
	if assets.TLSClientConfig == nil {
		assets.TLSClientConfig = &tls.Config{}
	}
	// Clone may carry the default transport's initialized h2 ALPN advertisement.
	// Keep the negotiated protocol consistent with this asset transport's H1 mode.
	assets.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return &toolpackHTTPTransport{metadata: metadata, assets: assets}
}

func (t *toolpackHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "release-assets.githubusercontent.com" || r.URL.Hostname() == "codeload.github.com" {
		return t.assets.RoundTrip(r)
	}
	return t.metadata.RoundTrip(r)
}

func (t *toolpackHTTPTransport) CloseIdleConnections() {
	t.metadata.CloseIdleConnections()
	t.assets.CloseIdleConnections()
}

type toolpackHTTPDownloadError struct{ status int }

type toolpackDownloadIdleError struct{ duration time.Duration }

func (e *toolpackDownloadIdleError) Error() string {
	return fmt.Sprintf("下载连续 %s 没有数据进展，已保留缓存", e.duration)
}
func (e *toolpackDownloadIdleError) Timeout() bool   { return true }
func (e *toolpackDownloadIdleError) Temporary() bool { return true }

func (e *toolpackHTTPDownloadError) Error() string {
	return fmt.Sprintf("下载源返回 HTTP %d，缓存已保留", e.status)
}

type toolpackDownloadIdentity struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type toolpackDownloadPart struct {
	Path   string
	Start  int64
	Length int64 // -1 means the whole file's length is not known yet
}

// Cache identity is tied to the official release URL, expected digest and size.
// Each part is contiguous, so only bytes actually written are resumed. No cached
// data is extracted or executed until the assembled file passes its full SHA-256.
func (m *toolpackManager) downloadResumable(ctx context.Context, a toolpackAsset, dest string) error {
	digest, err := hex.DecodeString(a.SHA256)
	if err != nil || len(digest) != sha256.Size || a.Size > 1<<30 || a.Size < 0 {
		return errors.New("发布包校验信息无效")
	}
	cache := filepath.Join(m.root, "cache", strings.ToLower(a.SHA256))
	if !withinToolpackRoot(m.root, cache) {
		return errors.New("下载缓存路径越界")
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	identity := toolpackDownloadIdentity{Source: a.URL, SHA256: strings.ToLower(a.SHA256), Size: a.Size}
	metadata := filepath.Join(cache, "identity.json")
	if data, err := os.ReadFile(metadata); err == nil {
		var saved toolpackDownloadIdentity
		if json.Unmarshal(data, &saved) != nil || saved != identity {
			return errors.New("下载缓存身份不符，请卸载工具缓存后重试")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		data, _ := json.Marshal(identity)
		if err := os.WriteFile(metadata, data, 0600); err != nil {
			return err
		}
	}
	completeSize, completeCached, err := completeToolpackCache(a, filepath.Join(cache, "whole.part"))
	if err != nil {
		return err
	}
	var parts []toolpackDownloadPart
	parallel := !completeCached && a.Size >= 8<<20
	if _, err := os.Stat(filepath.Join(cache, "no-ranges")); err == nil {
		parallel = false
	}
	if parallel {
		for i := int64(0); i < 4; i++ {
			start, end := a.Size*i/4, a.Size*(i+1)/4
			parts = append(parts, toolpackDownloadPart{Path: filepath.Join(cache, fmt.Sprintf("part-%d", i)), Start: start, Length: end - start})
		}
	} else {
		length := a.Size
		if completeCached {
			length = completeSize
		} else if length == 0 {
			length = -1
		}
		parts = []toolpackDownloadPart{{Path: filepath.Join(cache, "whole.part"), Length: length}}
	}
	total := a.Size
	if completeCached && total == 0 {
		total = completeSize
	}
	if err := m.setDownloadProgress(parts, total); err != nil {
		return err
	}
	if completeCached {
		m.mu.Lock()
		m.state.Stage = "完整下载缓存校验通过，准备解压"
		m.mu.Unlock()
	}
	if parallel {
		groupCtx, cancel := context.WithCancel(ctx)
		results := make(chan error, len(parts))
		for _, part := range parts {
			go func(p toolpackDownloadPart) {
				err := m.downloadPart(groupCtx, a, p, true)
				if err != nil {
					cancel()
				}
				results <- err
			}(part)
		}
		var first error
		var denied error
		unsupported := false
		for range parts {
			err := <-results
			var statusError *toolpackHTTPDownloadError
			if errors.As(err, &statusError) && statusError.status >= 400 && statusError.status < 500 {
				denied = err
			}
			if errors.Is(err, errToolpackRangeUnsupported) {
				unsupported = true
			}
			if err != nil && (first == nil || errors.Is(first, context.Canceled)) {
				first = err
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if denied != nil {
			return denied
		}
		if unsupported {
			if err := os.WriteFile(filepath.Join(cache, "no-ranges"), nil, 0600); err != nil {
				return err
			}
			parts = []toolpackDownloadPart{{Path: filepath.Join(cache, "whole.part"), Length: a.Size}}
			if err := m.setDownloadProgress(parts, a.Size); err != nil {
				return err
			}
			if err := m.downloadPart(ctx, a, parts[0], false); err != nil {
				return err
			}
		} else if first != nil {
			return first
		}
	} else if !completeCached {
		if err := m.downloadPart(ctx, a, parts[0], false); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var size int64
	for _, part := range parts {
		f, openErr := os.Open(part.Path)
		if openErr != nil {
			out.Close()
			return openErr
		}
		n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(f, 1<<30+1))
		f.Close()
		size += n
		if copyErr != nil {
			out.Close()
			return copyErr
		}
		if size > 1<<30 {
			out.Close()
			return errors.New("发布包超过 1 GiB 限制")
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if (a.Size > 0 && size != a.Size) || hex.EncodeToString(hash.Sum(nil)) != identity.SHA256 {
		_ = os.RemoveAll(cache)
		return errors.New("下载文件 SHA-256 校验失败，未安装；已丢弃损坏缓存，可重新安装")
	}
	// Completed archives are transient; installed programs and templates remain.
	_ = os.RemoveAll(cache)
	return nil
}

// A verified complete whole-file cache is useful even when the origin cannot
// resume ranges. The assembled destination is hashed again before extraction.
func completeToolpackCache(asset toolpackAsset, path string) (int64, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<30 {
		return 0, false, errors.New("下载缓存不是有效的普通文件")
	}
	if asset.Size > 0 && info.Size() != asset.Size {
		return info.Size(), false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, false, err
	}
	return info.Size(), strings.EqualFold(hex.EncodeToString(h.Sum(nil)), asset.SHA256), nil
}

func (m *toolpackManager) setDownloadProgress(parts []toolpackDownloadPart, total int64) error {
	var downloaded int64
	for _, p := range parts {
		info, err := os.Lstat(p.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("下载缓存不是普通文件")
		}
		if info.Size() > 1<<30 || (p.Length >= 0 && info.Size() > p.Length) {
			if err := os.Truncate(p.Path, 0); err != nil {
				return err
			}
		} else {
			downloaded += info.Size()
		}
	}
	m.mu.Lock()
	m.state.Downloaded = downloaded
	m.state.Total = total
	m.mu.Unlock()
	return nil
}

func (m *toolpackManager) addDownloaded(n int64) { m.mu.Lock(); m.state.Downloaded += n; m.mu.Unlock() }

func (m *toolpackManager) downloadPart(ctx context.Context, a toolpackAsset, p toolpackDownloadPart, bounded bool) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = m.downloadPartAttempt(ctx, a, p, bounded)
		if err == nil || ctx.Err() != nil {
			return err
		}
		var networkError net.Error
		if !errors.As(err, &networkError) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return err
}

func (m *toolpackManager) downloadPartAttempt(ctx context.Context, a toolpackAsset, p toolpackDownloadPart, bounded bool) (returnErr error) {
	f, err := os.OpenFile(p.Path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	offset := info.Size()
	if p.Length >= 0 && offset == p.Length {
		return nil
	}
	addr := a.DownloadURL
	if addr == "" {
		addr = a.URL
	}
	idle := m.downloadIdleTimeout
	if idle <= 0 {
		idle = 90 * time.Second
	}
	requestCtx, cancel := context.WithCancelCause(ctx)
	idleErr := &toolpackDownloadIdleError{duration: idle}
	watchdog := time.AfterFunc(idle, func() { cancel(idleErr) })
	defer func() {
		watchdog.Stop()
		if context.Cause(requestCtx) == idleErr {
			returnErr = idleErr
		}
		cancel(nil)
	}()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "prtseek-Personal-Toolpacks/1.0")
	setToolpackDownloadHeaders(req)
	if bounded {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.Start+offset, p.Start+p.Length-1))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	// Metadata has a short total deadline. A progressing package transfer has no
	// total cutoff; the watchdog only cancels a genuinely stalled attempt.
	downloadClient := *m.client
	downloadClient.Timeout = 0
	response, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	watchdog.Reset(idle)
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && !bounded {
		total, parseErr := strconv.ParseInt(strings.TrimPrefix(response.Header.Get("Content-Range"), "bytes */"), 10, 64)
		if parseErr == nil && total == offset {
			return nil
		}
		if parseErr == nil && total >= 0 && total < offset {
			if err := f.Truncate(0); err != nil {
				return err
			}
			m.addDownloaded(-offset)
			return io.ErrUnexpectedEOF
		}
		return errors.New("官方源拒绝续传范围；下载缓存与远端长度不符")
	}
	if response.StatusCode == http.StatusOK {
		if bounded {
			return errToolpackRangeUnsupported
		}
		if offset > 0 {
			if err := f.Truncate(0); err != nil {
				return err
			}
			m.addDownloaded(-offset)
			offset = 0
		}
	} else if response.StatusCode == http.StatusPartialContent {
		start, end, total, err := parseToolpackContentRange(response.Header.Get("Content-Range"))
		if err != nil || start != p.Start+offset || (a.Size > 0 && total != a.Size) || (bounded && end != p.Start+p.Length-1) || (response.ContentLength >= 0 && response.ContentLength != end-start+1) {
			return errors.New("官方源返回的续传范围与请求不一致，未写入数据")
		}
		if !bounded && p.Length < 0 {
			p.Length = total
			m.mu.Lock()
			m.state.Total = total
			m.mu.Unlock()
		}
	} else {
		return &toolpackHTTPDownloadError{status: response.StatusCode}
	}
	if response.ContentLength > 1<<30 {
		return errors.New("发布包超过 1 GiB 限制")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, 128<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			watchdog.Reset(idle)
			if offset+int64(n) > 1<<30 || (p.Length >= 0 && offset+int64(n) > p.Length) {
				return errors.New("下载响应超出预期大小")
			}
			written, writeErr := f.Write(buffer[:n])
			offset += int64(written)
			m.addDownloaded(int64(written))
			if writeErr != nil {
				return writeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = f.Sync()
			return readErr
		}
		if err := ctx.Err(); err != nil {
			_ = f.Sync()
			return err
		}
	}
	if p.Length >= 0 && offset != p.Length {
		return io.ErrUnexpectedEOF
	}
	return f.Sync()
}

func parseToolpackContentRange(value string) (start, end, total int64, err error) {
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, errors.New("missing byte range")
	}
	rangeValue, totalValue, ok := strings.Cut(strings.TrimPrefix(value, "bytes "), "/")
	if !ok {
		return 0, 0, 0, errors.New("invalid byte range")
	}
	startValue, endValue, ok := strings.Cut(rangeValue, "-")
	if !ok {
		return 0, 0, 0, errors.New("invalid byte range")
	}
	start, err = strconv.ParseInt(startValue, 10, 64)
	if err != nil {
		return
	}
	end, err = strconv.ParseInt(endValue, 10, 64)
	if err != nil {
		return
	}
	total, err = strconv.ParseInt(totalValue, 10, 64)
	if err != nil {
		return
	}
	if start < 0 || end < start || total <= end || total > 1<<30 {
		err = errors.New("invalid byte range bounds")
	}
	return
}
