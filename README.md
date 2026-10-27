# paperhunt

Search, download, and bundle academic papers from open-access repositories, preprint servers, and shadow libraries into local files.

Give `paperhunt` a search query, DOI, arXiv ID, bioRxiv/medRxiv URL, PubMed/PMC ID, OpenReview forum link, Cell Press / ScienceDirect URL, or Anna's Archive `/scidb/` link. It resolves the canonical metadata across OpenAlex, Europe PMC, Crossref, Unpaywall, arXiv, and OpenReview, checks for retractions and clinical trial registrations, and pulls a verified `%PDF-` file (plus `.tex` source, peer reviews, supplements, and linked code repositories when you want to replicate a paper).

## Install

A single static Go binary with zero runtime dependencies.

With [mise](https://mise.jdx.dev) (downloads the prebuilt release binary for macOS, Linux, or Windows):

```bash
mise use -g github:samhvw8/paperhunt
```

Or pin it in `~/.config/mise/config.toml`:

```toml
[tools]
"github:samhvw8/paperhunt" = "latest"
```

With Go:

```bash
go install github.com/samhvw8/paperhunt/cmd/paperhunt@latest
```

From source:

```bash
git clone https://github.com/samhvw8/paperhunt.git
cd paperhunt
go build -o paperhunt ./cmd/paperhunt
```

## Quick Start

```bash
# 1. Search across OpenAlex, Europe PMC, arXiv, OpenReview, and Crossref
paperhunt search "speculative decoding large language models"
paperhunt search --sort velocity --year 2024-2026 --oa-only "flow matching"
paperhunt search --journal "Nature Human Behaviour" --sort cited "decision making"

# Search and download the top 5 PDFs + BibTeX + metadata in parallel
paperhunt search -n 5 -d -o ./papers --meta --bib "mitochondrial dynamics"

# 2. Traverse backward references and forward citations around a seed paper
paperhunt snowball 1706.03762
paperhunt snowball --direction cited --sort velocity 10.1038/s41586-026-09951-7

# 3. Download one or more papers by DOI, URL, arXiv ID, PMCID, or OpenReview ID
paperhunt get 1706.03762 10.1038/nature14539 PMC8777576
paperhunt get -j 4 -o ./papers --meta --bib --reviews https://openreview.net/forum?id=taHwqSrbrb

# 4. Build a self-contained replication bundle (PDF + arXiv TeX + reviews + code repo)
paperhunt bundle --clone -d ./work 1706.03762

# 5. Inspect metadata, OA locations, retraction status, and BibTeX without downloading
paperhunt info --bib 10.1001/jamanetworkopen.2021.44279
```

## How the Download Waterfall Works

`paperhunt` tries open and preprint sources first, and only falls back to shadow libraries when every open route fails (or skips them entirely with `--oa-only`). Every downloaded response is verified for `%PDF-` magic bytes and a 4 KB minimum size so HTML login pages and bot challenges are never saved as `.pdf` files.

| Tier | Source | Details |
|---|---|---|
| 1. Direct & Preprint | arXiv, bioRxiv, medRxiv, PMC S3 (`pmc-oa-opendata`), ACL Anthology, PMLR, CVF, NeurIPS, HAL, Zenodo | Direct PDFs, `Accept-Encoding: identity` for bioRxiv/medRxiv Cloudflare buckets, and keyless AWS S3 downloads for PMC Open Access packages |
| 2. OpenReview | `api2.openreview.net` + `api.openreview.net` | Direct `/pdf?id=` when open, or title/DOI resolution to arXiv and OpenAlex preprints |
| 3. OA Aggregators | OpenAlex, Unpaywall, Europe PMC | Best open-access PDF locations and repository copies |
| 4. Title-to-Preprint Fallback | arXiv API + OpenAlex | When a paywalled publisher DOI has no direct OA link, checks for a matching preprint with the same normalized title and first author |
| 5. Shadow Fallback | Sci-Hub + Anna's Archive (`/scidb/`, `/md5/`) | Pure-Go SHA-256 Altcha proof-of-work solver for Sci-Hub (`sci-hub.ru`, `sci-hub.se`, `sci-hub.st`) and Anna's Archive fast download API when `ANNAS_SECRET_KEY` is set |

## What `paperhunt bundle` Creates

Running `paperhunt bundle --clone -d ./work <id-or-url>` creates `<slug>/`:

```text
<slug>/
├── paper.pdf        # Verified full-text PDF
├── paper.json       # Canonical metadata, IDs, retraction status, OA urls, discovered repos
├── paper.bib        # BibTeX entry
├── tex/             # Unpacked arXiv e-print source (.tex equations, .bib, figures)
├── reviews.md       # OpenReview submission, peer reviews, author rebuttals, meta-review
├── supp/            # PMC Open Access JATS XML and supplementary files
└── code/            # Shallow git clones (--depth 1) of GitHub/HuggingFace repos from the paper
```

## Optional Environment Variables

No API keys are required for normal use.

| Variable | Effect |
|---|---|
| `OPENREVIEW_TOKEN` or `OPENREVIEW_COOKIE` | Bypasses Cloudflare blocks on direct `api2.openreview.net/pdf` downloads |
| `ANNAS_SECRET_KEY` | Enables Anna's Archive `/dyn/api/fast_download.json` for `/md5/` hashes |
| `ANNA_COOKIE` | Passes a DDoS-Guard clearance cookie to `annas-archive.gd` |
| `UNPAYWALL_EMAIL` | Custom contact email for the Unpaywall API |
