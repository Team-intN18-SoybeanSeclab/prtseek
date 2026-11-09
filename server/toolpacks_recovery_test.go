package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolpackCompleteCacheSkipsNetwork(t *testing.T) {
	for _, knownSize := range []bool{false, true} {
		t.Run(fmt.Sprint(knownSize), func(t *testing.T) {
			payload := bytes.Repeat([]byte("verified"), 1<<20)
			m := newToolpackManager(t.TempDir())
			a := toolpackAsset{URL: "https://github.com/projectdiscovery/nuclei-templates/archive/refs/tags/v1.2.3.zip", SHA256: toolpackHash(payload)}
			if knownSize {
				a.Size = int64(len(payload))
			}
			seedToolpackDownload(t, m, a, map[string][]byte{"whole.part": payload})
			var requests atomic.Int32
			m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, errors.New("network must not be needed for a complete SHA-matching cache")
			})}
			dest := filepath.Join(t.TempDir(), "package.zip")
			if err := m.download(context.Background(), a, dest); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, payload) || requests.Load() != 0 {
				t.Fatal("verified full cache was not reused without network")
			}
		})
	}
}

func TestToolpackProgressingDownloadOutlivesMetadataTimeout(t *testing.T) {
	payload := []byte("slow-but-progressing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, b := range payload {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	m := newToolpackManager(t.TempDir())
	m.client.Timeout = 40 * time.Millisecond
	m.downloadIdleTimeout = 150 * time.Millisecond
	a := toolpackAsset{URL: srv.URL, SHA256: toolpackHash(payload), Size: int64(len(payload))}
	dest := filepath.Join(t.TempDir(), "package.zip")
	if err := m.download(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Fatal("progressing response was truncated")
	}
}

func TestToolpackStalledDownloadCancelsAndPreservesPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "abc")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	m := newToolpackManager(t.TempDir())
	m.downloadIdleTimeout = 80 * time.Millisecond
	part := filepath.Join(t.TempDir(), "whole.part")
	err := m.downloadPartAttempt(context.Background(), toolpackAsset{URL: srv.URL}, toolpackDownloadPart{Path: part, Length: -1}, false)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("stalled attempt was not a retryable timeout: %v", err)
	}
	got, readErr := os.ReadFile(part)
	if readErr != nil || string(got) != "abc" {
		t.Fatal("stalled response lost its written prefix")
	}
}

func TestToolpackTemplateRetryReusesVerifiedEngine(t *testing.T) {
	m := toolpackTestManager(t, false)
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	before := waitToolpack(t, m)
	original := m.client.Transport
	var engineDownloads atomic.Int32
	m.client.Transport = toolpackTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/projectdiscovery/nuclei/releases/assets/123" {
			engineDownloads.Add(1)
			return nil, errors.New("already installed engine was downloaded again")
		}
		return original.RoundTrip(r)
	})
	m.mu.Lock()
	m.state.Templates = toolpackComponent{}
	m.state.Enabled = false
	m.state.Status = "failed"
	m.mu.Unlock()
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	after := waitToolpack(t, m)
	if after.Status != "ready" || !after.Enabled || after.Engine.Path != before.Engine.Path || after.Templates.Path == "" || engineDownloads.Load() != 0 {
		t.Fatalf("template retry did not reuse engine: %+v", after)
	}
}
