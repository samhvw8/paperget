package paper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// BundleOptions configures what artifacts BuildBundle fetches into the bundle folder.
type BundleOptions struct {
	OutDir          string
	OAOnly          bool
	WithTeX         bool
	WithReviews     bool
	WithPMC         bool
	WithSupplements bool
	CloneCode       bool
	ArXivEprintBase string // optional override for tests
}

// BundleResult summarizes every file created inside a paper bundle directory.
type BundleResult struct {
	Dir         string   `json:"dir"`
	PDFPath     string   `json:"pdf_path,omitempty"`
	MetaPath    string   `json:"meta_path"`
	BibPath     string   `json:"bib_path"`
	ReviewsPath string   `json:"reviews_path,omitempty"`
	TeXFiles    []string `json:"tex_files,omitempty"`
	PMCFiles    []string `json:"pmc_files,omitempty"`
	CodeRepos   []string `json:"code_repos,omitempty"`
	ClonedRepo  string   `json:"cloned_repo,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	Meta        *Meta    `json:"meta"`
}

// BuildBundle resolves a paper and populates a self-contained directory for reading, appraisal, or replication.
func (r *Resolver) BuildBundle(ctx context.Context, ref Ref, opts BundleOptions) (*BundleResult, error) {
	meta, err := r.ResolveMeta(ctx, ref)
	if err != nil {
		return nil, err
	}

	baseDir := opts.OutDir
	if baseDir == "" {
		baseDir = "."
	}
	bundleDir := filepath.Join(baseDir, meta.Slug())
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return nil, fmt.Errorf("create bundle dir: %w", err)
	}

	res := &BundleResult{
		Dir:  bundleDir,
		Meta: meta,
	}

	pdfPath, pdfErr := r.DownloadPDF(ctx, meta, DownloadOptions{
		OutPath: filepath.Join(bundleDir, "paper.pdf"),
		OAOnly:  opts.OAOnly,
	})
	if pdfErr == nil {
		res.PDFPath = pdfPath
	} else {
		res.Warnings = append(res.Warnings, fmt.Sprintf("pdf: %v", pdfErr))
	}

	if opts.WithTeX && meta.ArXiv != "" {
		texDir := filepath.Join(bundleDir, "tex")
		texFiles, texErr := r.DownloadArXivTeX(ctx, meta, texDir, opts.ArXivEprintBase)
		if texErr == nil {
			res.TeXFiles = texFiles
			if len(meta.CodeRepos) == 0 {
				r.discoverReposFromTeXLinks(ctx, meta, texDir, texFiles)
			}
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("tex: %v", texErr))
		}
	}

	if opts.WithReviews {
		revMD, revErr := r.FetchOpenReviewThread(ctx, meta)
		if revErr == nil && revMD != "" {
			revPath := filepath.Join(bundleDir, "reviews.md")
			if err := os.WriteFile(revPath, []byte(revMD), 0o644); err == nil {
				res.ReviewsPath = revPath
			}
		}
	}

	if opts.WithPMC && meta.PMCID != "" {
		pmcDir := filepath.Join(bundleDir, "pmc")
		pmcFiles, pmcErr := r.DownloadPMCArtifacts(ctx, meta.PMCID, pmcDir, opts.WithSupplements)
		if pmcErr == nil {
			res.PMCFiles = pmcFiles
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("pmc: %v", pmcErr))
		}
	}

	bibPath := filepath.Join(bundleDir, "paper.bib")
	if err := os.WriteFile(bibPath, []byte(meta.BibTeX()), 0o644); err == nil {
		res.BibPath = bibPath
	}

	res.CodeRepos = meta.CodeRepos
	if len(meta.CodeRepos) > 0 {
		reposRaw, _ := json.MarshalIndent(map[string]any{
			"paper_title":  meta.Title,
			"arxiv":        meta.ArXiv,
			"doi":          meta.DOI,
			"repositories": meta.CodeRepos,
		}, "", "  ")
		_ = os.WriteFile(filepath.Join(bundleDir, "code-repos.json"), reposRaw, 0o644)
	}

	if opts.CloneCode && len(meta.CodeRepos) > 0 {
		repoURL := meta.CodeRepos[0]
		codeDir := filepath.Join(bundleDir, "code")
		cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		cmd := exec.CommandContext(cloneCtx, "git", "clone", "--depth", "1", repoURL, codeDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("git clone %s: %v (%s)", repoURL, err, strings.TrimSpace(string(out))))
		} else {
			res.ClonedRepo = codeDir
		}
		cancel()
	}

	metaPath := filepath.Join(bundleDir, "paper.json")
	if rawMeta, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, rawMeta, 0o644)
		res.MetaPath = metaPath
	}

	return res, nil
}

// DownloadArXivTeX downloads https://arxiv.org/e-print/<id> and unpacks TeX/bib files into destDir.
func (r *Resolver) DownloadArXivTeX(ctx context.Context, m *Meta, destDir, overrideBase string) ([]string, error) {
	base := "https://arxiv.org/e-print"
	if overrideBase != "" {
		base = strings.TrimRight(overrideBase, "/")
	}
	u := fmt.Sprintf("%s/%s", base, url.PathEscape(m.ArXiv))
	raw, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	if len(raw) < 64 {
		return nil, fmt.Errorf("e-print payload too short (%d bytes)", len(raw))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}

	payload := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		decompressed, err := io.ReadAll(io.LimitReader(gz, 100<<20))
		gz.Close()
		if err != nil {
			return nil, fmt.Errorf("gzip decompress: %w", err)
		}
		payload = decompressed
	}

	tr := tar.NewReader(bytes.NewReader(payload))
	var extracted []string
	isTar := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		isTar = true
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		cleanRel := filepath.Clean(hdr.Name)
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			continue
		}
		target := filepath.Join(destDir, cleanRel)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(tr, 25<<20))
		if err != nil {
			continue
		}
		if err := os.WriteFile(target, content, 0o644); err == nil {
			extracted = append(extracted, cleanRel)
			ext := strings.ToLower(filepath.Ext(cleanRel))
			if ext == ".tex" || ext == ".bbl" || ext == ".md" {
				m.extractReposFromText(string(content))
			}
		}
	}

	if !isTar && len(payload) > 0 && !bytes.HasPrefix(payload, []byte("%PDF-")) {
		texFile := filepath.Join(destDir, "main.tex")
		if err := os.WriteFile(texFile, payload, 0o644); err != nil {
			return nil, err
		}
		m.extractReposFromText(string(payload))
		extracted = append(extracted, "main.tex")
	}

	if len(extracted) == 0 {
		return nil, fmt.Errorf("no TeX source files in e-print response")
	}
	sort.Strings(extracted)
	return extracted, nil
}

// DownloadPMCArtifacts downloads JATS XML, plain text, and optional supplements from pmc-oa-opendata S3.
func (r *Resolver) DownloadPMCArtifacts(ctx context.Context, pmcid, destDir string, includeSupplements bool) ([]string, error) {
	u := fmt.Sprintf("%s/?list-type=2&prefix=%s.", r.PMCS3Base, url.QueryEscape(strings.ToUpper(pmcid)))
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	var bucket struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &bucket); err != nil {
		return nil, err
	}
	if len(bucket.Contents) == 0 {
		return nil, fmt.Errorf("no objects in pmc-oa-opendata for %s", pmcid)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}

	var saved []string
	for _, obj := range bucket.Contents {
		base := filepath.Base(obj.Key)
		ext := strings.ToLower(filepath.Ext(base))
		if ext == ".pdf" {
			continue
		}
		isCore := ext == ".xml" || ext == ".txt" || ext == ".json"
		if !isCore && !includeSupplements {
			continue
		}
		data, err := r.getBytes(ctx, fmt.Sprintf("%s/%s", r.PMCS3Base, obj.Key), nil)
		if err != nil {
			continue
		}
		target := filepath.Join(destDir, base)
		if err := os.WriteFile(target, data, 0o644); err == nil {
			saved = append(saved, base)
		}
	}
	return saved, nil
}

// FetchOpenReviewThread retrieves the submission and public review thread from OpenReview v2/v1.
func (r *Resolver) FetchOpenReviewThread(ctx context.Context, m *Meta) (string, error) {
	if m.OpenReviewForum == "" && m.Title != "" {
		u := fmt.Sprintf("%s/notes/search?term=%s&type=terms&content=all&source=forum&limit=15", r.OpenReviewV2, url.QueryEscape(m.Title))
		var res struct {
			Notes []openReviewNote `json:"notes"`
		}
		if err := r.getJSON(ctx, u, openReviewAuthHeaders(), &res); err == nil {
			normT := normTitle(m.Title)
			for _, n := range res.Notes {
				t := orStr(n.Content, "title")
				if t != "" && normTitle(t) == normT {
					m.OpenReviewForum = n.Forum
					if len(n.Signatures) > 0 {
						if idx := strings.LastIndex(n.Signatures[0], "/"); idx > 0 {
							m.OpenReviewGroup = n.Signatures[0][:idx]
						}
					}
					break
				}
			}
		}
	}
	if m.OpenReviewForum == "" {
		return "", nil
	}

	notes := m.orNotes
	hdrs := openReviewAuthHeaders()

	if len(hdrs) > 0 {
		for _, base := range []string{r.OpenReviewV2, r.OpenReviewV1} {
			u := fmt.Sprintf("%s/notes?forum=%s", base, url.QueryEscape(m.OpenReviewForum))
			var res struct {
				Notes []openReviewNote `json:"notes"`
			}
			if err := r.getJSON(ctx, u, hdrs, &res); err == nil && len(res.Notes) > 0 {
				notes = res.Notes
				break
			}
		}
	}

	if len(notes) == 0 {
		for _, base := range r.openReviewBasesForGroup(m.OpenReviewGroup) {
			var qURL string
			if m.OpenReviewGroup != "" {
				grp := strings.TrimSuffix(m.OpenReviewGroup, "/") + "/"
				q := url.Values{
					"term":    {orGroupTerms},
					"type":    {"terms"},
					"content": {"all"},
					"source":  {"all"},
					"group":   {grp},
					"limit":   {"250"},
				}
				qURL = base + "/notes/search?" + q.Encode()
			} else {
				q := url.Values{
					"term":    {m.OpenReviewForum},
					"type":    {"terms"},
					"content": {"all"},
					"source":  {"all"},
					"limit":   {"50"},
				}
				qURL = base + "/notes/search?" + q.Encode()
			}
			var res struct {
				Notes []openReviewNote `json:"notes"`
			}
			if err := r.getJSON(ctx, qURL, nil, &res); err == nil {
				for _, n := range res.Notes {
					if n.Forum == m.OpenReviewForum {
						notes = append(notes, n)
					}
				}
				if len(notes) > 0 {
					break
				}
			}
		}
	}

	if len(notes) == 0 {
		return "", fmt.Errorf("no OpenReview notes found for forum %s", m.OpenReviewForum)
	}

	sort.SliceStable(notes, func(i, j int) bool {
		return notes[i].Cdate < notes[j].Cdate
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# OpenReview Forum: %s\n\n", m.Title)
	fmt.Fprintf(&b, "- **Forum URL:** https://openreview.net/forum?id=%s\n", m.OpenReviewForum)
	if m.Venue != "" {
		fmt.Fprintf(&b, "- **Venue:** %s\n", m.Venue)
	}
	b.WriteString("\n---\n\n")

	reviewFields := []string{
		"rating", "recommendation", "decision", "confidence", "soundness", "presentation", "contribution",
		"summary", "strengths", "weaknesses", "questions", "limitations", "review", "comment", "metareview",
	}

	for _, n := range notes {
		if n.ID == m.OpenReviewForum && n.Replyto == "" {
			continue
		}
		sig := "Reviewer"
		if len(n.Signatures) > 0 {
			parts := strings.Split(n.Signatures[0], "/")
			sig = parts[len(parts)-1]
		}
		title := orStr(n.Content, "title")
		if title == "" {
			title = sig
		}
		fmt.Fprintf(&b, "## %s (%s)\n\n", title, sig)
		for _, field := range reviewFields {
			if val := orStr(n.Content, field); val != "" {
				fmt.Fprintf(&b, "**%s:** %s\n\n", field, val)
			}
		}
		b.WriteString("---\n\n")
	}

	return b.String(), nil
}

var reTeXURL = regexp.MustCompile(`(?:\\href|\\url)\{(https?://[^\s\}"]+)\}`)

func (r *Resolver) discoverReposFromTeXLinks(ctx context.Context, m *Meta, texDir string, texFiles []string) {
	seen := map[string]bool{}
	var links []string
	for _, rel := range texFiles {
		if !strings.HasSuffix(strings.ToLower(rel), ".tex") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(texDir, rel))
		if err != nil {
			continue
		}
		for _, match := range reTeXURL.FindAllStringSubmatch(string(raw), -1) {
			u := strings.TrimSpace(match[1])
			low := strings.ToLower(u)
			if seen[u] || strings.Contains(low, "doi.org") || strings.Contains(low, "arxiv.org") || strings.Contains(low, "creativecommons.org") {
				continue
			}
			seen[u] = true
			links = append(links, u)
			if len(links) >= 3 {
				break
			}
		}
	}
	for _, link := range links {
		body, err := r.getBytes(ctx, link, nil)
		if err != nil {
			continue
		}
		r := strings.NewReplacer("%3A", ":", "%3a", ":", "%2F", "/", "%2f", "/")
		text := r.Replace(string(body))
		m.extractReposFromText(text)
		if len(m.CodeRepos) > 0 {
			return
		}
	}
}
