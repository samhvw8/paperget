package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samhvw8/paperget/internal/paper"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "-h", "--help", "help":
		usage()
		return
	case "get", "download":
		runGet(os.Args[2:])
	case "bundle", "replicate":
		runBundle(os.Args[2:])
	case "info", "resolve":
		runInfo(os.Args[2:])
	default:
		runGet(os.Args[1:])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `paperget — Academic paper downloader & replication bundle builder

Usage:
  paperget [get] [flags] <id-or-url-or-query>...
  paperget bundle [flags] <id-or-url-or-query>
  paperget info [flags] <id-or-url-or-query>

Supported inputs:
  - DOI (10.1038/nature14539, https://doi.org/10.1371/journal.pmed.1003680)
  - arXiv (1706.03762, arXiv:2312.00752, https://arxiv.org/abs/1706.03762, https://huggingface.co/papers/1706.03762)
  - bioRxiv / medRxiv (https://www.biorxiv.org/content/10.1101/2020.05.22.111161v1)
  - OpenReview (https://openreview.net/forum?id=taHwqSrbrb, taHwqSrbrb@ICLR.cc/2025/Conference/Submission2019)
  - PubMed / PMC / Europe PMC (PMC8478176, PMID:34582448, https://pmc.ncbi.nlm.nih.gov/articles/PMC8478176/)
  - ACL Anthology, PMLR, CVF, NeurIPS, HAL, Zenodo
  - Anna's Archive / Sci-Hub URLs or MD5 (https://annas-archive.gd/scidb/10.1038/nature14539, md5:<32-hex>)
  - Paper title query ("Denoising Diffusion Implicit Models")

Flags for 'get':
  -o string        Output PDF file path or directory (default ".")
  --oa-only        Use only legal open-access & preprint routes (skip Sci-Hub & Anna's Archive)
  --meta           Write <slug>.json metadata sidecar next to PDF
  --bib            Write <slug>.bib BibTeX sidecar next to PDF
  --reviews        Write <slug>.reviews.md OpenReview sidecar if available
  --json           Print JSON result to stdout

Flags for 'bundle' (alias 'replicate'):
  -d string        Parent directory for the bundle folder (default ".")
  --oa-only        Skip shadow-library routes
  --tex            Download and unpack arXiv LaTeX e-print source (default true)
  --reviews        Fetch OpenReview peer reviews into reviews.md (default true)
  --pmc            Fetch PMC JATS XML & text from S3 (default true)
  --supplements    Also download PMC supplementary files
  --clone          Shallow-clone primary discovered GitHub/HuggingFace repository into code/
  --json           Print JSON result to stdout
`)
}

func runGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	outFlag := fs.String("o", "", "Output file or directory")
	oaOnly := fs.Bool("oa-only", false, "Open-access routes only (skip shadow libraries)")
	writeMeta := fs.Bool("meta", false, "Write .json metadata sidecar")
	writeBib := fs.Bool("bib", false, "Write .bib BibTeX sidecar")
	writeReviews := fs.Bool("reviews", false, "Write .reviews.md OpenReview sidecar")
	jsonOut := fs.Bool("json", false, "Print JSON summary")
	_ = fs.Parse(reorderFlags(args))

	targets := fs.Args()
	if len(targets) == 0 {
		usage()
		os.Exit(2)
	}

	resolver := paper.NewResolver()
	var results []*paper.Meta
	exitCode := 0

	for _, target := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		ref := paper.Parse(target)
		meta, err := resolver.ResolveMeta(ctx, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error resolving %s: %v\n", target, err)
			cancel()
			exitCode = 1
			continue
		}

		dlOpts := paper.DownloadOptions{OAOnly: *oaOnly}
		if *outFlag != "" {
			if len(targets) == 1 && strings.HasSuffix(strings.ToLower(*outFlag), ".pdf") {
				dlOpts.OutPath = *outFlag
			} else {
				dlOpts.OutDir = *outFlag
			}
		}

		pdfPath, err := resolver.DownloadPDF(ctx, meta, dlOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error downloading %s (%s): %v\n", target, meta.Title, err)
			cancel()
			exitCode = 1
			continue
		}

		baseNoExt := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath))
		if *writeMeta {
			if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
				_ = os.WriteFile(baseNoExt+".json", b, 0o644)
			}
		}
		if *writeBib {
			_ = os.WriteFile(baseNoExt+".bib", []byte(meta.BibTeX()), 0o644)
		}
		if *writeReviews {
			if rev, err := resolver.FetchOpenReviewThread(ctx, meta); err == nil && rev != "" {
				_ = os.WriteFile(baseNoExt+".reviews.md", []byte(rev), 0o644)
			}
		}
		cancel()

		results = append(results, meta)
		if !*jsonOut {
			retractWarn := ""
			if meta.Retracted {
				retractWarn = " [WARNING: RETRACTED]"
			}
			fmt.Printf("Saved %s (%d KB via %s)%s\n", pdfPath, meta.SavedPDFBytes/1024, meta.SavedPDFSource, retractWarn)
			if meta.Title != "" {
				fmt.Printf("  Title: %s (%s)\n", meta.Title, meta.Year)
			}
			if len(meta.CodeRepos) > 0 {
				fmt.Printf("  Code:  %s\n", strings.Join(meta.CodeRepos, ", "))
			}
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if len(results) == 1 {
			_ = enc.Encode(results[0])
		} else {
			_ = enc.Encode(results)
		}
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func runBundle(args []string) {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	outDir := fs.String("d", ".", "Parent directory for bundle")
	oaOnly := fs.Bool("oa-only", false, "Open-access routes only")
	withTeX := fs.Bool("tex", true, "Download arXiv TeX source")
	withReviews := fs.Bool("reviews", true, "Download OpenReview peer reviews")
	withPMC := fs.Bool("pmc", true, "Download PMC JATS XML & text")
	withSupp := fs.Bool("supplements", false, "Download PMC supplementary files")
	cloneCode := fs.Bool("clone", false, "Shallow-clone primary discovered code repo")
	jsonOut := fs.Bool("json", false, "Print JSON output")
	_ = fs.Parse(reorderFlags(args))

	if fs.NArg() == 0 {
		usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	resolver := paper.NewResolver()
	ref := paper.Parse(fs.Arg(0))
	res, err := resolver.BuildBundle(ctx, ref, paper.BundleOptions{
		OutDir:          *outDir,
		OAOnly:          *oaOnly,
		WithTeX:         *withTeX,
		WithReviews:     *withReviews,
		WithPMC:         *withPMC,
		WithSupplements: *withSupp,
		CloneCode:       *cloneCode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}

	fmt.Printf("Bundle created: %s\n", res.Dir)
	if res.PDFPath != "" {
		fmt.Printf("  PDF:     %s (%d KB via %s)\n", res.PDFPath, res.Meta.SavedPDFBytes/1024, res.Meta.SavedPDFSource)
	}
	if len(res.TeXFiles) > 0 {
		fmt.Printf("  TeX:     %d files in %s/tex/\n", len(res.TeXFiles), res.Dir)
	}
	if res.ReviewsPath != "" {
		fmt.Printf("  Reviews: %s\n", res.ReviewsPath)
	}
	if len(res.PMCFiles) > 0 {
		fmt.Printf("  PMC:     %s\n", strings.Join(res.PMCFiles, ", "))
	}
	if len(res.CodeRepos) > 0 {
		fmt.Printf("  Code:    %s\n", strings.Join(res.CodeRepos, ", "))
	}
	if res.ClonedRepo != "" {
		fmt.Printf("  Cloned:  %s\n", res.ClonedRepo)
	}
	for _, w := range res.Warnings {
		fmt.Printf("  Note:    %s\n", w)
	}
}

func runInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Print JSON output")
	_ = fs.Parse(reorderFlags(args))

	if fs.NArg() == 0 {
		usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resolver := paper.NewResolver()
	ref := paper.Parse(fs.Arg(0))
	meta, err := resolver.ResolveMeta(ctx, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "info error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(meta)
		return
	}

	fmt.Printf("Title:     %s\n", meta.Title)
	if len(meta.Authors) > 0 {
		fmt.Printf("Authors:   %s\n", strings.Join(meta.Authors, ", "))
	}
	fmt.Printf("Venue:     %s (%s)\n", meta.Venue, meta.Year)
	fmt.Printf("Retracted: %v\n", meta.Retracted)
	for _, n := range meta.RetractionNotices {
		fmt.Printf("  Notice:  %s\n", n)
	}
	fmt.Printf("IDs:       DOI=%s arXiv=%s PMID=%s PMCID=%s OpenReview=%s\n",
		meta.DOI, meta.ArXiv, meta.PMID, meta.PMCID, meta.OpenReviewForum)
	if len(meta.CodeRepos) > 0 {
		fmt.Printf("Code:      %s\n", strings.Join(meta.CodeRepos, ", "))
	}
	fmt.Printf("OA PDF Candidates (%d):\n", len(meta.PDFCandidates))
	for _, c := range meta.PDFCandidates {
		fmt.Printf("  [%d] %s -> %s\n", c.Priority, c.Source, c.URL)
	}
}

func reorderFlags(args []string) []string {
	var flags, pos []string
	valueFlags := map[string]bool{"-o": true, "-d": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}
