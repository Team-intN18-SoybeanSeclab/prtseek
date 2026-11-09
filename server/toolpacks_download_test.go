package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolpackTransportNegotiatesMetadataAndAssets(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) }))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	transport := newToolpackHTTPTransport()
	defer transport.CloseIdleConnections()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	for _, test := range []struct {
		name      string
		transport *http.Transport
		major     int
	}{{"metadata", transport.metadata, 2}, {"assets", transport.assets, 1}} {
		t.Run(test.name, func(t *testing.T) {
			test.transport.TLSClientConfig.RootCAs = roots
			client := &http.Client{Transport: test.transport}
			response, err := client.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.ProtoMajor != test.major {
				t.Fatalf("negotiated %s", response.Proto)
			}
		})
	}
}

func seedToolpackDownload(t *testing.T, m *toolpackManager, a toolpackAsset, parts map[string][]byte) string {
	t.Helper()
	cache := filepath.Join(m.root, "cache", a.SHA256)
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(toolpackDownloadIdentity{Source: a.URL, SHA256: a.SHA256, Size: a.Size})
	if err := os.WriteFile(filepath.Join(cache, "identity.json"), identity, 0600); err != nil {
		t.Fatal(err)
	}
	for name, data := range parts {
		if err := os.WriteFile(filepath.Join(cache, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return cache
}

func TestToolpackParallelDownloadResumesAndVerifies(t *testing.T) {
	payload := make([]byte, 8<<20+13)
	for i := range payload {
		payload[i] = byte(i*31 + i/8192)
	}
	var current, peak, requests atomic.Int32
	var requestedBytes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		defer current.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		requests.Add(1)
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requestedBytes.Add(end - start + 1)
		time.Sleep(5 * time.Millisecond)
		http.ServeContent(w, r, "asset.zip", time.Unix(1, 0), bytes.NewReader(payload))
	}))
	defer srv.Close()
	m := newToolpackManager(t.TempDir())
	a := toolpackAsset{URL: srv.URL, SHA256: toolpackHash(payload), Size: int64(len(payload))}
	prefix := 131071
	part2start, part2end := len(payload)*2/4, len(payload)*3/4
	cache := seedToolpackDownload(t, m, a, map[string][]byte{"part-0": payload[:prefix], "part-2": payload[part2start:part2end]})
	dest := filepath.Join(t.TempDir(), "download.zip")
	if err := m.download(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("assembled data differs")
	}
	if requestedBytes.Load() != int64(len(payload)-prefix-(part2end-part2start)) {
		t.Fatal("cached prefix or completed part was downloaded again")
	}
	if requests.Load() != 3 || peak.Load() > 4 {
		t.Fatalf("unexpected concurrency or requests: %d/%d", peak.Load(), requests.Load())
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("verified archive left its download cache")
	}
}

func TestToolpackDownloadHandlesRangeResponses(t *testing.T) {
	payload := []byte("official-verified-download-fixture")
	for _, mode := range []string{"resume206", "restart200", "complete416", "reset416", "bad206", "rate429", "corrupt-cache"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				switch mode {
				case "restart200":
					_, _ = w.Write(payload)
					return
				case "complete416", "reset416":
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
						w.WriteHeader(416)
						return
					}
				case "bad206":
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
					w.WriteHeader(206)
					_, _ = w.Write(payload)
					return
				case "rate429":
					w.WriteHeader(429)
					return
				}
				if call > 3 {
					t.Error("unbounded retries")
				}
				http.ServeContent(w, r, "asset.zip", time.Unix(1, 0), bytes.NewReader(payload))
			}))
			defer srv.Close()
			m := newToolpackManager(t.TempDir())
			a := toolpackAsset{URL: srv.URL, SHA256: toolpackHash(payload)}
			prefix := append([]byte(nil), payload[:5]...)
			if mode == "complete416" {
				prefix = payload
			}
			if mode == "reset416" {
				prefix = append(append([]byte(nil), payload...), []byte("extra")...)
			}
			if mode == "corrupt-cache" {
				prefix = []byte("xxxxx")
			}
			cache := seedToolpackDownload(t, m, a, map[string][]byte{"whole.part": prefix})
			if mode == "complete416" {
				err := m.downloadPartAttempt(context.Background(), a, toolpackDownloadPart{Path: filepath.Join(cache, "whole.part"), Length: -1}, false)
				if err != nil || calls.Load() != 1 {
					t.Fatalf("complete 416 response was not accepted: %v", err)
				}
				return
			}
			dest := filepath.Join(t.TempDir(), "download.zip")
			err := m.download(context.Background(), a, dest)
			if mode == "bad206" || mode == "rate429" || mode == "corrupt-cache" {
				if err == nil {
					t.Fatal("invalid response or cache was accepted")
				}
				if calls.Load() != 1 {
					t.Fatal("repeated a denied or invalid request")
				}
				if mode == "corrupt-cache" {
					if _, err := os.Stat(cache); !os.IsNotExist(err) {
						t.Fatal("corrupt cache was retained")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("range response produced wrong bytes")
			}
		})
	}
}

func TestToolpackParallelDownloadFallsBackWhenRangeIgnored(t *testing.T) {
	payload := bytes.Repeat([]byte("verified"), 1<<20)
	var fullRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			fullRequests.Add(1)
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	m := newToolpackManager(t.TempDir())
	a := toolpackAsset{URL: srv.URL, SHA256: toolpackHash(payload), Size: int64(len(payload))}
	dest := filepath.Join(t.TempDir(), "download.zip")
	if err := m.download(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) || fullRequests.Load() != 1 {
		t.Fatal("range fallback was not a single complete download")
	}
}

func TestToolpackParallelDownloadDoesNotFallbackAfterRateLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("verified"), 1<<20)
	var fullRequests atomic.Int32
	m := newToolpackManager(t.TempDir())
	a := toolpackAsset{URL: "https://github.com/projectdiscovery/nuclei/releases/download/v1.2.3/fixture.zip", SHA256: toolpackHash(payload), Size: int64(len(payload))}
	m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") == "" {
			fullRequests.Add(1)
		}
		status := http.StatusOK
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}
	if err := m.download(context.Background(), a, filepath.Join(t.TempDir(), "download.zip")); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("rate limit not preserved: %v", err)
	}
	if fullRequests.Load() != 0 {
		t.Fatal("download retried through full-file fallback after HTTP 429")
	}
}

type toolpackCancelledBody struct {
	data   *bytes.Reader
	cancel context.CancelFunc
}

func (b *toolpackCancelledBody) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if err == io.EOF {
		b.cancel()
		return 0, context.Canceled
	}
	return n, err
}
func (b *toolpackCancelledBody) Close() error { return nil }

func TestToolpackCancelledDownloadKeepsPrefix(t *testing.T) {
	payload := []byte("resumable-official-package")
	m := newToolpackManager(t.TempDir())
	a := toolpackAsset{URL: "https://github.com/projectdiscovery/nuclei/releases/download/v1.2.3/test.zip", SHA256: toolpackHash(payload)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: &toolpackCancelledBody{data: bytes.NewReader(payload[:6]), cancel: cancel}}, nil
	})}
	dest := filepath.Join(t.TempDir(), "download.zip")
	if err := m.download(ctx, a, dest); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	part := filepath.Join(m.root, "cache", a.SHA256, "whole.part")
	data, err := os.ReadFile(part)
	if err != nil || string(data) != string(payload[:6]) {
		t.Fatal("cancelled prefix lost")
	}
	m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "bytes=6-" {
			t.Fatal("retry did not resume")
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{fmt.Sprintf("bytes 6-%d/%d", len(payload)-1, len(payload))}}, ContentLength: int64(len(payload) - 6), Body: io.NopCloser(strings.NewReader(string(payload[6:])))}, nil
	})}
	if err := m.download(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
}
