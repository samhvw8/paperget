package paper

import (
	"bytes"
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
	"regexp"
	"strings"
	"time"
)

const (
	MinPDFBytes = 4096
	MaxPDFBytes = 250 << 20 // 250 MB cap
)

// DownloadOptions configures how DownloadPDF selects and saves a paper's PDF.
type DownloadOptions struct {
	OutPath    string
	OutDir     string
	OAOnly     bool
	SciHubBase string // optional override for testing
	AnnaBase   string // optional override for testing
}

var (
	reCitationPDF = regexp.MustCompile(`(?i)<meta[^>]+name=["']citation_pdf_url["'][^>]+content=["']([^"']+)["']`)
	reCitationRev = regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+name=["']citation_pdf_url["']`)
	reEmbedPDF    = regexp.MustCompile(`(?i)(?:src|href)=["']([^"']+\.pdf(?:\?[^"'#]*)?)(?:#[^"']*)?["']`)
)

// DownloadPDF runs the multi-tier waterfall until a verified PDF is saved to disk.
func (r *Resolver) DownloadPDF(ctx context.Context, m *Meta, opts DownloadOptions) (string, error) {
	if m == nil {
		return "", errors.New("nil paper metadata")
	}

	var errs []string
	var pdfData []byte
	var winningSource string

	for _, cand := range m.PDFCandidates {
		data, src, err := r.tryFetchPDF(ctx, cand.URL, cand.Source, cand.Headers, true)
		if err == nil {
			pdfData = data
			winningSource = src
			break
		}
		errs = append(errs, fmt.Sprintf("%s (%s): %v", cand.Source, cand.URL, err))
	}

	if len(pdfData) == 0 && !opts.OAOnly {
		data, src, shadowErr := r.downloadFromShadow(ctx, m, opts)
		if shadowErr == nil && len(data) > 0 {
			pdfData = data
			winningSource = src
		} else if shadowErr != nil {
			errs = append(errs, fmt.Sprintf("shadow: %v", shadowErr))
		}
	}

	if len(pdfData) == 0 {
		if len(errs) == 0 {
			return "", errors.New("no PDF download routes found for this paper")
		}
		return "", fmt.Errorf("all PDF download routes failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	outPath := opts.OutPath
	if outPath == "" {
		dir := opts.OutDir
		if dir == "" {
			dir = "."
		}
		outPath = filepath.Join(dir, m.Slug()+".pdf")
	} else if info, err := os.Stat(outPath); err == nil && info.IsDir() {
		outPath = filepath.Join(outPath, m.Slug()+".pdf")
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}

	sum := sha256.Sum256(pdfData)
	tmpPath := outPath + ".tmp"
	if err := os.WriteFile(tmpPath, pdfData, 0o644); err != nil {
		return "", fmt.Errorf("write PDF: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename PDF: %w", err)
	}

	m.SavedPDFPath = outPath
	m.SavedPDFSource = winningSource
	m.SavedPDFSHA256 = hex.EncodeToString(sum[:])
	m.SavedPDFBytes = int64(len(pdfData))

	if len(m.CodeRepos) == 0 {
		m.extractReposFromPDFBytes(pdfData)
	}

	return outPath, nil
}

func (r *Resolver) tryFetchPDF(ctx context.Context, rawURL, source string, hdrs map[string]string, followHTMLMeta bool) ([]byte, string, error) {
	dlCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/pdf,application/octet-stream;q=0.9,*/*;q=0.8")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 45 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxPDFBytes))
	if err != nil {
		return nil, "", err
	}

	if cleaned, ok := VerifyAndTrimPDF(data); ok {
		return cleaned, source, nil
	}

	if followHTMLMeta && looksLikeHTML(data) {
		if embeddedURL := extractPDFLinkFromHTML(string(data), resp.Request.URL); embeddedURL != "" && embeddedURL != rawURL {
			return r.tryFetchPDF(ctx, embeddedURL, source+"+meta", hdrs, false)
		}
	}

	return nil, "", fmt.Errorf("not a valid PDF (size=%d bytes, content-type=%q)", len(data), resp.Header.Get("Content-Type"))
}

// VerifyAndTrimPDF verifies that data contains the %PDF- header within the first
// 1024 bytes and is at least MinPDFBytes long, returning the slice starting at %PDF-.
func VerifyAndTrimPDF(data []byte) ([]byte, bool) {
	if len(data) < MinPDFBytes {
		return nil, false
	}
	window := data
	if len(window) > 1024 {
		window = window[:1024]
	}
	idx := bytes.Index(window, []byte("%PDF-"))
	if idx < 0 {
		return nil, false
	}
	trimmed := data[idx:]
	if len(trimmed) < MinPDFBytes {
		return nil, false
	}
	return trimmed, true
}

func looksLikeHTML(data []byte) bool {
	sample := strings.ToLower(string(data[:min(len(data), 512)]))
	return strings.Contains(sample, "<!doctype html") || strings.Contains(sample, "<html") || strings.Contains(sample, "<head")
}

func extractPDFLinkFromHTML(htmlStr string, base *url.URL) string {
	for _, re := range []*regexp.Regexp{reCitationPDF, reCitationRev, reEmbedPDF} {
		if m := re.FindStringSubmatch(htmlStr); m != nil {
			cand := strings.TrimSpace(m[1])
			if cand == "" {
				continue
			}
			if strings.HasPrefix(cand, "//") {
				return "https:" + cand
			}
			if base != nil {
				if resolved, err := base.Parse(cand); err == nil {
					return resolved.String()
				}
			}
			return cand
		}
	}
	return ""
}

func (m *Meta) extractReposFromPDFBytes(pdfData []byte) {
	for _, match := range reCodeRepo.FindAllSubmatch(pdfData, -1) {
		full := string(match[0])
		parts := strings.Split(string(match[1]), "/")
		if len(parts) >= 2 && parts[0] != "papers" && parts[0] != "datasets" && parts[0] != "spaces" {
			m.addRepo(full)
		}
	}
}
