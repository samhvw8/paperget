package paper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIdentifiers(t *testing.T) {
	cases := []struct {
		in      string
		checkFn func(t *testing.T, r Ref)
	}{
		{
			in: "10.1038/nature14539",
			checkFn: func(t *testing.T, r Ref) {
				if r.DOI != "10.1038/nature14539" {
					t.Fatalf("got DOI %q", r.DOI)
				}
			},
		},
		{
			in: "https://www.cell.com/trends/cognitive-sciences/abstract/S1364-6613(16)30098-5",
			checkFn: func(t *testing.T, r Ref) {
				if r.PII != "S1364661316300985" {
					t.Fatalf("expected PII S1364661316300985, got %+v", r)
				}
			},
		},
		{
			in: "https://annas-archive.gd/scidb/10.1038/nature14539",
			checkFn: func(t *testing.T, r Ref) {
				if r.DOI != "10.1038/nature14539" {
					t.Fatalf("expected scidb DOI, got %+v", r)
				}
			},
		},
		{
			in: "https://annas-archive.gd/md5/0123456789abcdef0123456789abcdef",
			checkFn: func(t *testing.T, r Ref) {
				if r.AnnaMD5 != "0123456789abcdef0123456789abcdef" {
					t.Fatalf("expected AnnaMD5, got %+v", r)
				}
			},
		},
		{
			in: "https://www.biorxiv.org/content/10.64898/2026.09.10.750628v1.full.pdf",
			checkFn: func(t *testing.T, r Ref) {
				if r.DOI != "10.64898/2026.09.10.750628" || r.RxivServer != "biorxiv" || r.RxivVersion != "v1" {
					t.Fatalf("unexpected 10.64898 biorxiv parse: %+v", r)
				}
			},
		},
		{
			in: "https://www.biorxiv.org/content/10.1101/2020.05.22.111161v2.full.pdf",
			checkFn: func(t *testing.T, r Ref) {
				if r.DOI != "10.1101/2020.05.22.111161" || r.RxivServer != "biorxiv" || r.RxivVersion != "v2" {
					t.Fatalf("unexpected biorxiv parse: %+v", r)
				}
			},
		},
		{
			in: "https://huggingface.co/papers/1706.03762",
			checkFn: func(t *testing.T, r Ref) {
				if r.ArXiv != "1706.03762" {
					t.Fatalf("expected arXiv 1706.03762, got %+v", r)
				}
			},
		},
		{
			in: "https://openreview.net/forum?id=taHwqSrbrb",
			checkFn: func(t *testing.T, r Ref) {
				if r.OpenReviewForum != "taHwqSrbrb" {
					t.Fatalf("expected OpenReview forum taHwqSrbrb, got %+v", r)
				}
			},
		},
		{
			in: "taHwqSrbrb@ICLR.cc/2025/Conference/Submission2019",
			checkFn: func(t *testing.T, r Ref) {
				if r.OpenReviewForum != "taHwqSrbrb" || r.OpenReviewGroup != "ICLR.cc/2025/Conference/Submission2019" {
					t.Fatalf("unexpected OpenReview ref: %+v", r)
				}
			},
		},
		{
			in: "https://openaccess.thecvf.com/content_CVPR_2020/html/He_Momentum_Contrast_CVPR_2020_paper.html",
			checkFn: func(t *testing.T, r Ref) {
				want := "https://openaccess.thecvf.com/content_CVPR_2020/papers/He_Momentum_Contrast_CVPR_2020_paper.pdf"
				if r.DirectPDF != want {
					t.Fatalf("got DirectPDF %q, want %q", r.DirectPDF, want)
				}
			},
		},
	}

	for _, tc := range cases {
		tc.checkFn(t, Parse(tc.in))
	}
}

func TestVerifyAndTrimPDF(t *testing.T) {
	htmlPage := []byte("<!DOCTYPE html><html><head><title>DDoS-Guard</title></head><body>" + strings.Repeat("x", 6000))
	if _, ok := VerifyAndTrimPDF(htmlPage); ok {
		t.Fatal("expected HTML page to be rejected")
	}

	shortPDF := []byte("%PDF-1.7\nshort")
	if _, ok := VerifyAndTrimPDF(shortPDF); ok {
		t.Fatal("expected short PDF to be rejected")
	}

	validBody := append([]byte("   \n%PDF-1.7\n"), bytes.Repeat([]byte("0"), MinPDFBytes+100)...)
	trimmed, ok := VerifyAndTrimPDF(validBody)
	if !ok {
		t.Fatal("expected valid PDF to be accepted")
	}
	if !bytes.HasPrefix(trimmed, []byte("%PDF-1.7")) {
		t.Fatalf("expected trimmed PDF to start with %%PDF-1.7, got %q", string(trimmed[:16]))
	}
}

func TestSciHubAltchaSolverAndDownload(t *testing.T) {
	salt := "testsalt123?expires=1999999999&"
	secretNum := 4219
	sum := sha256.Sum256([]byte(salt + "4219"))
	challengeHex := hex.EncodeToString(sum[:])

	fakePDF := append([]byte("%PDF-1.7\nhttps://github.com/example/deep-learning-code\n"), bytes.Repeat([]byte("A"), MinPDFBytes+500)...)
	solvedCookie := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/10.1038/nature14539":
			if !strings.Contains(r.Header.Get("Cookie"), "verified=1") {
				http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "sess123", Path: "/"})
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(`<html><body><altcha-widget challengeurl="/captcha/challenge/999"></altcha-widget></body></html>`))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><embed src="/storage/2024/Deep-learning.pdf#view=FitH"></body></html>`))

		case "/captcha/challenge/999":
			_ = json.NewEncoder(w).Encode(altchaChallenge{
				Algorithm: "SHA-256",
				Challenge: challengeHex,
				MaxNumber: 10000,
				Salt:      salt,
				Signature: "sig123",
			})

		case "/captcha/solution/999":
			var body struct {
				Captcha string `json:"captcha"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw, _ := base64.StdEncoding.DecodeString(body.Captcha)
			var sol altchaSolution
			_ = json.Unmarshal(raw, &sol)
			if sol.Number == secretNum {
				solvedCookie = true
				http.SetCookie(w, &http.Cookie{Name: "verified", Value: "1", Path: "/"})
				_, _ = w.Write([]byte(`{"success":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":false}`))

		case "/storage/2024/Deep-learning.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(fakePDF)
		}
	}))
	defer srv.Close()

	resolver := NewResolver()
	meta := &Meta{
		Title:   "Deep Learning",
		Year:    "2015",
		Authors: []string{"Yann LeCun"},
		DOI:     "10.1038/nature14539",
	}
	tmpDir := t.TempDir()
	outPath, err := resolver.DownloadPDF(context.Background(), meta, DownloadOptions{
		OutDir:     tmpDir,
		SciHubBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("DownloadPDF failed: %v", err)
	}
	if !solvedCookie {
		t.Fatal("expected Altcha challenge to be solved")
	}
	if meta.SavedPDFSource != "scihub" {
		t.Fatalf("expected SavedPDFSource=scihub, got %q", meta.SavedPDFSource)
	}
	if len(meta.CodeRepos) != 1 || meta.CodeRepos[0] != "https://github.com/example/deep-learning-code" {
		t.Fatalf("expected repo extracted from PDF bytes, got %v", meta.CodeRepos)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected saved PDF at %s: %v", outPath, err)
	}
}

func TestArXivTeXExtractionAndPathTraversalDefense(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	files := map[string]string{
		"main.tex":           "\\documentclass{article}\n% Code at https://github.com/org/awesome-paper.git\n\\begin{document}Hello\\end{document}",
		"../../etc/evil.txt": "should-be-dropped",
	}
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-eprint-tar")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	resolver := NewResolver()
	meta := &Meta{ArXiv: "1706.03762"}
	dest := filepath.Join(t.TempDir(), "tex")

	extracted, err := resolver.DownloadArXivTeX(context.Background(), meta, dest, srv.URL)
	if err != nil {
		t.Fatalf("DownloadArXivTeX error: %v", err)
	}
	if len(extracted) != 1 || extracted[0] != "main.tex" {
		t.Fatalf("expected only main.tex extracted, got %v", extracted)
	}
	if len(meta.CodeRepos) != 1 || meta.CodeRepos[0] != "https://github.com/org/awesome-paper" {
		t.Fatalf("expected GitHub repo extracted from main.tex, got %v", meta.CodeRepos)
	}
}
