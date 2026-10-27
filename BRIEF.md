# Ambition Brief: `paperget` → `paperhunt`

## Goal Question (Altitude)

I read the goal as turning `paperget` into **`paperhunt`** — a single compiled CLI and skill suite that hunts (searches, deduplicates, ranks) and downloads/bundles papers across open-access and shadow repositories. Which altitude do you want?

- **Option A (Federated Search + Parallel Download CLI & Skills):** Rename `paperget` → `paperhunt` (`~/workspace/tools/paperhunt`, `github.com/samhvw8/paperhunt`), add `paperhunt search <query>` with concurrent multi-engine fan-out (`OpenAlex`, `Europe PMC`, `arXiv`, `OpenReview`, `Crossref`), canonical deduplication, Reciprocal Rank Fusion + citation-velocity ranking (`--sort cited|velocity|date|relevance`, `--journal`, `--year`, `--oa-only`), parallel batch downloads (`-j 4`, `--download`), and title-to-preprint fallback for paywalled publisher DOIs; update `paper-download`, `paper-replicate`, and `paper-appraisal`.
- **Option B (Option A + Citation-Graph Snowballing):** Everything in Option A, plus `paperhunt snowball <id-or-url> [--direction refs|cited|both] [--limit N] [--download]` to traverse a paper's backward references and forward citations via OpenAlex and rank/download the foundational and follow-up papers around any seed paper.

*(Access note for higher rungs: Anna's Archive fast-download API requires `ANNAS_SECRET_KEY` and OpenReview direct PDF API requires `OPENREVIEW_TOKEN` when Cloudflare Turnstile / DDoS-Guard blocks anonymous direct access.)*

---

## Plan

- **Raw material:**
  - Live API payloads from **OpenAlex** (`api.openalex.org/works` & `/sources`), **Europe PMC** (`ebi.ac.uk/europepmc/webservices/rest/search`), **arXiv** (`export.arxiv.org/api/query`), **OpenReview** (`api2.openreview.net/notes/search`), and **Crossref** (`api.crossref.org/works`).
  - Raw PDF streams (`%PDF-1.x` header + byte-length verification) from PMC AWS S3 (`pmc-oa-opendata`), arXiv, bioRxiv/medRxiv, Unpaywall/OpenAlex OA locations, and Sci-Hub (Altcha SHA-256 PoW).
- **Instruments:**
  - **Federated RRF + Citation-Velocity Ranker:** Concurrent fan-out across engines, canonical entity clustering (merging records by normalized DOI, arXiv base ID, PMID, PMCID, or normalized title+year key), and Reciprocal Rank Fusion ($\text{RRF}(d) = \sum_{e} \frac{1}{60 + r_e(d)}$) combined with citation velocity ($\text{citations}/\max(1, \text{age})$).
  - **Venue/Journal Resolver:** Resolves `--journal "<name>"` (e.g. `"Nature Human Behaviour"`, `"PNAS"`, `"Trends in Cognitive Sciences"`) to OpenAlex `source.id` so journal-scoped queries return exact matches without HTTP 400 filter errors.
  - **Two-Source Preprint Fallback:** When a closed-access publisher DOI has no direct OA PDF and is not yet in Sci-Hub, automatically query arXiv, Europe PMC, and OpenAlex by exact normalized title + first author to locate an unlinked preprint copy.
  - **Bounded Concurrent Worker Pool (`-j N`):** Parallelizes multi-paper downloads (`paperhunt get -j 4 ...` and `paperhunt search --download -j 4 ...`) with atomic temp-file writes.
- **Fields:**
  1. **Information Retrieval:** Multi-engine federation, canonical deduplication, and Reciprocal Rank Fusion (RRF).
  2. **Bibliometrics / Scientometrics:** Citation-velocity normalization ($\text{citations}/\text{year}$) and venue/retraction metadata enrichment.
  3. **Investigative Journalism (`deal.py`):** Two-source verification — cross-matching paywalled publisher DOIs against preprint repositories by exact title + author.
  4. **Systems Engineering:** Bounded concurrent download pool (`-j N`) and strict `%PDF-` byte validation.
- **Axes:**
  - **Depth:** `rung 0 → 2` — Move from single-ID lookup to federated 5-engine search, canonical entity deduplication, RRF + citation-velocity ranking, and title-to-preprint fallback.
  - **Speed:** `rung 0 → 2` — Collapse the multi-step "write Python search script → copy DOIs → sequential download" loop into one parallel command (`paperhunt search "<query>" --download -j 4`).
  - **Proof:** `rung 1 → 3` — Verify against both `mise run check` (`go test -race ./...` + `okf_validate.py`) and a held-out live benchmark across 5 search domains and 8 multi-source PDF downloads.

---

## Contract

| Line | Content |
|---|---|
| **Judge** | 1. `mise run check` in `~/workspace/tools/paperhunt` + `mise run dot:check` + `ccp doctor` in `~/.dotfiles` (all exit `0`).<br>2. `self-built` live benchmark against external APIs (outside check: live responses from OpenAlex, Europe PMC, arXiv, OpenReview, Crossref, PMC S3, bioRxiv, Sci-Hub):<br>• `paperhunt search` across 5 held-out queries (CS/AI, mitochondria biology, clinical trial, cognitive science with `--journal`, ML OpenReview) with `--json`, returning $\ge 5$ deduplicated results per query in $< 5\text{ s}$.<br>• `paperhunt search --download -j 4` + `paperhunt get -j 4` across 8 held-out papers (arXiv, bioRxiv `10.64898/`, PMC S3, OpenReview, Cell Press PII, closed-access *Nature* / *PNAS*), verifying `%PDF-` header and size $\ge 50\text{ KB}$ on disk. |
| **Target** | `5/5` federated search queries passing in `< 5s` each + `8/8` live held-out PDFs verified on disk + `>= 8` unit tests passing under `go test -race` (`unreasonable on purpose` vs. baseline of `0` search commands, sequential-only `get`, and `4` unit tests). |
| **Stop line** | 3 real approaches to cross-engine ranking/deduplication and unlinked-preprint fallback before reporting a miss. |
| **Scope** | • Rename `~/workspace/tools/paperget` → `~/workspace/tools/paperhunt`, module `github.com/samhvw8/paperhunt`, binary `~/.local/bin/paperhunt` (remove `~/.local/bin/paperget`), and GitHub repo `samhvw8/paperget` → `samhvw8/paperhunt`.<br>• Add `internal/paper/search.go` (`paperhunt search` with `--source`, `--sort cited|velocity|date|relevance`, `--year`, `--journal`, `--oa-only`, `--limit`, `--json`, `--download`).<br>• Add title-to-preprint OA fallback (`internal/paper/download.go`) and parallel worker pool `-j N` (`cmd/paperhunt/main.go`).<br>• (If Option B chosen) Add `paperhunt snowball <id>` in `internal/paper/search.go` & `cmd/paperhunt/main.go`.<br>• Update `.okf/` in `~/workspace/tools/paperhunt` and skills `paper-download`, `paper-replicate`, `paper-appraisal` in `~/.dotfiles/home/.ccp/hub/skills/`, sync `ccp`, and push both repos. |
| **Runs** | `nothing new` (stateless local Go CLI binary `~/.local/bin/paperhunt`; zero background processes or metered dependencies). |
