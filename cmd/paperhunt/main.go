package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/samhvw8/paperhunt/internal/paper"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "-h", "--help", "help":
		usage()
		return
	case "-v", "--version", "version":
		fmt.Printf("paperhunt %s\n", version)
		return
	case "search", "s", "find":
		if err := runSearch(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "snowball", "graph", "cite":
		if err := runSnowball(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "get", "dl", "download":
		if err := runGet(args); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	case "bundle", "replicate":
		if err := runBundle(args); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	case "info", "meta", "resolve":
		if err := runInfo(args); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	default:
		if err := runGet(os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `paperhunt — multi-source academic paper search, downloader, and replication bundle builder

Usage:
  paperhunt search   [flags] <query...>
  paperhunt snowball [flags] <id-or-url>
  paperhunt [get]    [flags] <id-or-url-or-query...>
  paperhunt bundle   [flags] <id-or-url-or-query>
  paperhunt info     [flags] <id-or-url-or-query>

Subcommands:
  search     Federated search across OpenAlex, Europe PMC, arXiv, OpenReview, and Crossref
             with RRF + citation-velocity ranking, filters (--journal, --year, --oa-only),
             and optional parallel PDF download (-d / --download).
  snowball   Traverse backward references and forward citations around a seed paper via OpenAlex.
  get        Download verified PDFs (and optional .json, .bib, .reviews.md sidecars) in parallel (-j N).
  bundle     Create a self-contained paper directory (paper.pdf, tex/, supp/, reviews.md, code/).
  info       Resolve and print canonical metadata, OA locations, retraction status, and BibTeX.
`)
}

func reorderFlags(args []string, boolFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && !boolFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	source := fs.String("source", "all", "Comma-separated engines: all,openalex,epmc,arxiv,openreview,crossref")
	sortMode := fs.String("sort", "rrf", "Sort order: rrf (default), cited, velocity, date")
	year := fs.String("year", "", "Publication year or range (e.g. 2025, 2022-2026, 2024-)")
	journal := fs.String("journal", "", "Filter by journal or venue name (e.g. 'Nature Human Behaviour')")
	oaOnly := fs.Bool("oa-only", false, "Only return Open Access papers")
	limit := fs.Int("limit", 10, "Maximum number of deduplicated results")
	asJSON := fs.Bool("json", false, "Emit JSON array")
	download := fs.Bool("download", false, "Download the search results in parallel")
	dlShort := fs.Bool("d", false, "Alias for --download")
	outDir := fs.String("o", ".", "Output directory when --download / -d is set")
	writeMeta := fs.Bool("meta", false, "Write .json metadata sidecar when downloading")
	writeBib := fs.Bool("bib", false, "Write .bib BibTeX sidecar when downloading")
	jobs := fs.Int("j", 4, "Parallel download workers")

	bools := map[string]bool{"oa-only": true, "json": true, "download": true, "d": true, "meta": true, "bib": true}
	if err := fs.Parse(reorderFlags(args, bools)); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("usage: paperhunt search [flags] <query>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := paper.NewResolver()

	results, err := r.Search(ctx, paper.SearchOptions{
		Query:   query,
		Sources: []string{*source},
		Sort:    *sortMode,
		Year:    *year,
		Journal: *journal,
		OAOnly:  *oaOnly,
		Limit:   *limit,
	})
	if err != nil {
		return err
	}

	if *asJSON && !*download && !*dlShort {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	if !*asJSON {
		printSearchTable(results)
	}

	if *download || *dlShort {
		var targets []string
		for _, it := range results {
			targets = append(targets, it.BestID())
		}
		return downloadBatch(ctx, r, targets, *outDir, *oaOnly, *writeMeta, *writeBib, false, *asJSON, *jobs)
	}
	return nil
}

func runSnowball(args []string) error {
	fs := flag.NewFlagSet("snowball", flag.ContinueOnError)
	direction := fs.String("direction", "both", "Traversal direction: both (default), refs (backward), cited (forward)")
	sortMode := fs.String("sort", "cited", "Sort order: cited (default), velocity, date")
	oaOnly := fs.Bool("oa-only", false, "Only include Open Access papers")
	limit := fs.Int("limit", 10, "Maximum number of papers to return")
	asJSON := fs.Bool("json", false, "Emit JSON object")
	download := fs.Bool("download", false, "Download the snowballed papers in parallel")
	dlShort := fs.Bool("d", false, "Alias for --download")
	outDir := fs.String("o", ".", "Output directory when --download / -d is set")
	writeMeta := fs.Bool("meta", false, "Write .json metadata sidecar when downloading")
	writeBib := fs.Bool("bib", false, "Write .bib BibTeX sidecar when downloading")
	jobs := fs.Int("j", 4, "Parallel download workers")

	bools := map[string]bool{"oa-only": true, "json": true, "download": true, "d": true, "meta": true, "bib": true}
	if err := fs.Parse(reorderFlags(args, bools)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: paperhunt snowball [flags] <id-or-url>")
	}
	seed := strings.TrimSpace(strings.Join(fs.Args(), " "))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := paper.NewResolver()

	res, err := r.Snowball(ctx, seed, paper.SnowballOptions{
		Direction: *direction,
		Sort:      *sortMode,
		OAOnly:    *oaOnly,
		Limit:     *limit,
	})
	if err != nil {
		return err
	}

	if *asJSON && !*download && !*dlShort {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	if !*asJSON {
		fmt.Printf("Seed: %s (%s) [OpenAlex: %s | Cited by: %d]\n\n", res.Seed.Title, res.Seed.Year, res.OpenAlexID, res.Seed.CitedBy)
		printSearchTable(res.Items)
	}

	if *download || *dlShort {
		var targets []string
		for _, it := range res.Items {
			targets = append(targets, it.BestID())
		}
		return downloadBatch(ctx, r, targets, *outDir, *oaOnly, *writeMeta, *writeBib, false, *asJSON, *jobs)
	}
	return nil
}

func printSearchTable(items []paper.SearchResult) {
	if len(items) == 0 {
		fmt.Println("No matching papers found.")
		return
	}
	for i, it := range items {
		dirTag := ""
		if it.Direction != "" {
			dirTag = fmt.Sprintf(" [%s]", strings.ToUpper(it.Direction))
		}
		oaTag := "closed"
		if it.IsOA {
			oaTag = "OA"
		}
		venue := it.Venue
		if venue == "" {
			venue = strings.Join(it.Sources, "+")
		}
		fmt.Printf("%2d. [%d | cited:%d | %.1f/yr | %s]%s %s\n",
			i+1, it.Year, it.CitedBy, it.CitationVelocity, oaTag, dirTag, it.Title)
		fmt.Printf("    ID: %-42s  Venue: %s  (%s)\n",
			it.BestID(), venue, strings.Join(it.Sources, ","))
	}
}

func runGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	outFlag := fs.String("o", "", "Output directory or file path")
	oaOnly := fs.Bool("oa-only", false, "Only use Open Access sources (skip shadow libraries)")
	writeMeta := fs.Bool("meta", false, "Write .json metadata sidecar")
	writeBib := fs.Bool("bib", false, "Write .bib BibTeX sidecar")
	writeReviews := fs.Bool("reviews", false, "Write .reviews.md OpenReview peer-review thread")
	asJSON := fs.Bool("json", false, "Emit JSON output")
	jobs := fs.Int("j", 4, "Parallel download workers")

	bools := map[string]bool{"oa-only": true, "meta": true, "bib": true, "reviews": true, "json": true}
	if err := fs.Parse(reorderFlags(args, bools)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: paperhunt get [flags] <id-or-url...>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	r := paper.NewResolver()
	return downloadBatch(ctx, r, fs.Args(), *outFlag, *oaOnly, *writeMeta, *writeBib, *writeReviews, *asJSON, *jobs)
}

func downloadBatch(ctx context.Context, r *paper.Resolver, inputs []string, outFlag string, oaOnly, writeMeta, writeBib, writeReviews, asJSON bool, jobs int) error {
	if jobs <= 0 {
		jobs = 1
	}
	if jobs > 8 {
		jobs = 8
	}

	type itemOutcome struct {
		input   string
		pdfPath string
		meta    *paper.Meta
		err     error
	}
	outcomes := make([]itemOutcome, len(inputs))
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup

	for idx, raw := range inputs {
		idx, raw := idx, raw
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			itemCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()

			ref := paper.Parse(raw)
			meta, err := r.ResolveMeta(itemCtx, ref)
			if err != nil {
				outcomes[idx] = itemOutcome{input: raw, err: fmt.Errorf("error resolving %s: %w", raw, err)}
				return
			}
			dlOpts := paper.DownloadOptions{OAOnly: oaOnly}
			if outFlag != "" {
				if len(inputs) == 1 && strings.HasSuffix(strings.ToLower(outFlag), ".pdf") {
					dlOpts.OutPath = outFlag
				} else {
					dlOpts.OutDir = outFlag
				}
			}

			pdfPath, err := r.DownloadPDF(itemCtx, meta, dlOpts)
			if err != nil {
				outcomes[idx] = itemOutcome{input: raw, meta: meta, err: fmt.Errorf("error downloading %s (%s): %w", raw, meta.Title, err)}
				return
			}
			baseNoExt := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath))
			if writeMeta {
				if b, mErr := json.MarshalIndent(meta, "", "  "); mErr == nil {
					_ = os.WriteFile(baseNoExt+".json", b, 0o644)
				}
			}
			if writeBib {
				_ = os.WriteFile(baseNoExt+".bib", []byte(meta.BibTeX()), 0o644)
			}
			if writeReviews {
				if rev, revErr := r.FetchOpenReviewThread(itemCtx, meta); revErr == nil && rev != "" {
					_ = os.WriteFile(baseNoExt+".reviews.md", []byte(rev), 0o644)
				}
			}
			outcomes[idx] = itemOutcome{input: raw, pdfPath: pdfPath, meta: meta}
		}()
	}
	wg.Wait()

	var (
		metas []*paper.Meta
		errs  []string
	)
	for _, o := range outcomes {
		if o.err != nil {
			errs = append(errs, o.err.Error())
			continue
		}
		metas = append(metas, o.meta)
		if !asJSON {
			retractWarn := ""
			if o.meta.Retracted {
				retractWarn = " [WARNING: RETRACTED]"
			}
			fmt.Printf("Saved %s (%d KB via %s)%s\n", o.pdfPath, o.meta.SavedPDFBytes/1024, o.meta.SavedPDFSource, retractWarn)
			if o.meta.Title != "" {
				fmt.Printf("  Title: %s (%s)\n", o.meta.Title, o.meta.Year)
			}
			if len(o.meta.CodeRepos) > 0 {
				fmt.Printf("  Code:  %s\n", strings.Join(o.meta.CodeRepos, ", "))
			}
		}
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if len(metas) == 1 {
			_ = enc.Encode(metas[0])
		} else {
			_ = enc.Encode(metas)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	outDir := fs.String("d", ".", "Parent directory for bundle")
	oaOnly := fs.Bool("oa-only", false, "Open-access routes only")
	withTeX := fs.Bool("tex", true, "Download arXiv TeX source")
	withReviews := fs.Bool("reviews", true, "Download OpenReview peer reviews")
	withPMC := fs.Bool("pmc", true, "Download PMC JATS XML & text")
	withSupp := fs.Bool("supplements", false, "Download PMC supplementary files")
	cloneCode := fs.Bool("clone", false, "Shallow-clone primary discovered code repo")
	jsonOut := fs.Bool("json", false, "Print JSON output")

	bools := map[string]bool{"oa-only": true, "tex": true, "reviews": true, "pmc": true, "supplements": true, "clone": true, "json": true}
	if err := fs.Parse(reorderFlags(args, bools)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: paperhunt bundle [flags] <id-or-url>")
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
		return fmt.Errorf("bundle error: %w", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
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
	return nil
}

func runInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "Print JSON output")
	bibOut := fs.Bool("bib", false, "Print BibTeX citation")

	bools := map[string]bool{"json": true, "bib": true}
	if err := fs.Parse(reorderFlags(args, bools)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: paperhunt info [flags] <id-or-url>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resolver := paper.NewResolver()
	ref := paper.Parse(fs.Arg(0))
	meta, err := resolver.ResolveMeta(ctx, ref)
	if err != nil {
		return fmt.Errorf("info error: %w", err)
	}

	if *bibOut {
		fmt.Print(meta.BibTeX())
		return nil
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(meta)
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
	return nil
}
