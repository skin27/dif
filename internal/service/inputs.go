package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"dif/api"
)

type Document struct {
	Name string
	Data []byte
	Info *api.FlowInfo
}

// Resolve reads immutable snapshots. URLs are labeled by index so diagnostics
// never expose credentials, signed query strings or remote response bodies.
func Resolve(ctx context.Context, o Options) ([]Document, error) {
	type resolution struct {
		docs []Document
		err  error
	}
	done := make(chan resolution, 1)
	go func() { docs, err := resolve(ctx, o, http.DefaultTransport); done <- resolution{docs, err} }()
	select {
	case result := <-done:
		return result.docs, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func resolve(ctx context.Context, o Options, transport http.RoundTripper) ([]Document, error) {
	paths := append([]string(nil), o.Files...)
	for _, dir := range o.Dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read flow directory: %w", err)
		}
		n := len(paths)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			// Follow visible ConfigMap projection symlinks, but never directories.
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path)
			if err != nil {
				return nil, fmt.Errorf("stat flow file: %w", err)
			}
			if info.Mode().IsRegular() {
				paths = append(paths, path)
			}
		}
		if len(paths) == n {
			return nil, fmt.Errorf("flow directory %q contains no visible JSON files", dir)
		}
	}
	var docs []Document
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.Ext(path) == "" {
			path += ".json"
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("flow %q must be a regular file", path)
		}
		data, err := readBounded(f, o.MaxBytes)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("flow %q: %w", path, err)
		}
		docs = append(docs, Document{Name: path, Data: data})
	}
	client := &http.Client{Transport: transport, Timeout: duration(o.FetchTimeout), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("unsafe redirect")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return errors.New("cross-host redirects are disabled")
		}
		return nil
	}}
	for i, raw := range o.URLs {
		name := fmt.Sprintf("URL #%d", i+1)
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("%s must be an HTTPS raw document URL without userinfo or fragment", name)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("%s is invalid", name)
		}
		req.Header.Set("Accept", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s fetch failed (network, TLS, redirect or timeout)", name)
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return nil, fmt.Errorf("%s returned HTTP %d", name, res.StatusCode)
		}
		data, err := readBounded(res.Body, o.MaxBytes)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if o.SHA256 != "" {
			want, err := hex.DecodeString(o.SHA256)
			got := sha256.Sum256(data)
			if err != nil || len(want) != len(got) || !strings.EqualFold(o.SHA256, hex.EncodeToString(got[:])) {
				return nil, fmt.Errorf("%s SHA-256 mismatch or invalid digest", name)
			}
		}
		docs = append(docs, Document{Name: name, Data: data})
	}
	ids := map[string]string{}
	for i := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := api.InspectBytes(docs[i].Data)
		if err != nil {
			return nil, fmt.Errorf("%s: static validation failed: %w", docs[i].Name, err)
		}
		if previous, ok := ids[info.ID]; ok {
			return nil, fmt.Errorf("duplicate flow id %q in %s and %s", info.ID, previous, docs[i].Name)
		}
		ids[info.ID] = docs[i].Name
		docs[i].Info = info
	}
	if len(docs) == 0 {
		return nil, errors.New("no flow documents")
	}
	return docs, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, errors.New("document read failed")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("document exceeds max-bytes")
	}
	return data, nil
}
