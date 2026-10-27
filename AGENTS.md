# paperhunt

Standalone Go CLI tool for resolving academic paper identifiers, downloading verified PDFs through an open-access and shadow-library waterfall, and assembling replication bundles (PDF, BibTeX, metadata JSON, arXiv LaTeX source, PMC supplements, OpenReview peer reviews, and linked code repositories).

## Layout

- `cmd/paperhunt/main.go` — CLI entrypoint (`get`, `bundle`/`replicate`, `info`).
- `internal/paper/ids.go` — Identifier & URL parser (DOI, arXiv, bioRxiv, medRxiv, OpenReview, PMID, PMCID, ACL, PMLR, CVF, NeurIPS, HAL, Zenodo, Anna's Archive MD5, title queries).
- `internal/paper/meta.go` — Parallel metadata cross-resolution (OpenAlex, Europe PMC, Crossref, Unpaywall, Semantic Scholar, arXiv API, bioRxiv API, OpenReview v2/v1 APIs) and BibTeX generation.
- `internal/paper/download.go` — Multi-tier PDF waterfall with `%PDF-` header verification.
- `internal/paper/shadow.go` — Pure-HTTP Sci-Hub/Sci-Net SHA-256 Altcha PoW solver with persistent cookie cache, plus Anna's Archive (`/scidb/`, `/md5/`, and fast-download API).
- `internal/paper/bundle.go` — Replication bundle assembler: arXiv `e-print` TeX source extraction, PMC S3 (`pmc-oa-opendata`) JATS/supplements, OpenReview thread markdown, and GitHub/Hugging Face code repository discovery and cloning.

## Commands

- `mise run build` — build `bin/paperhunt`
- `mise run install` — install `paperhunt` to `~/.local/bin/paperhunt` and Go bin
- `mise run test` — run offline race tests
- `mise run check` — format check, vet, race tests, and strict OKF validation
- `mise run docs:validate` — validate `.okf/` bundle

## Rules

- Keep `Parallax` (`~/workspace/parallax`) free of shadow-library routes; shadow-library fallback (`Sci-Hub`, `Anna's Archive`) belongs only in `paperhunt` (and is skipped when `--oa-only` is passed).
- Verify every downloaded PDF starts with `%PDF-` and exceeds the minimum size floor before writing to disk; never save HTML challenge pages as `.pdf`.
- Project knowledge lives in `.okf/`. Update it in the same change as the code it describes, and run the validator.
