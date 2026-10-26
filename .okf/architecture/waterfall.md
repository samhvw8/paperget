---
type: Architecture
title: Multi-tier PDF download waterfall
description: Resolution order from preprint servers and keyless S3 buckets through OA aggregators to Sci-Hub Altcha PoW and Anna's Archive.
resource: internal/paper/download.go
tags: [waterfall, pdf, arxiv, biorxiv, pmc, openreview, scihub, annas-archive]
status: stable
generated: { by: omp-agent/gemini-4-argon, at: '2026-10-26T15:00:00Z' }
sources:
  - id: download
    resource: internal/paper/download.go
    title: Waterfall orchestration and PDF verification
  - id: shadow
    resource: internal/paper/shadow.go
    title: Sci-Hub Altcha PoW solver and Anna's Archive client
---

# Resolution Order

Every candidate response is checked for `%PDF-` magic bytes and a minimum size floor (4 KB) so HTML challenge pages are never saved as PDFs.[^download]

| Tier | Source | Mechanism |
|---|---|---|
| 1. Preprint & Direct OA | arXiv, bioRxiv/medRxiv, PMC S3, ACL, PMLR, CVF, NeurIPS, HAL, Zenodo | `arxiv.org/pdf/<id>.pdf`; `biorxiv.org/content/<doi>.full.pdf` with `Accept-Encoding: identity`; `pmc-oa-opendata.s3.amazonaws.com` keyless S3 listing |
| 2. OpenReview | `api2.openreview.net` + `api.openreview.net` | Direct `/pdf?id=<forum>` when `OPENREVIEW_TOKEN`/`OPENREVIEW_COOKIE` is set; otherwise resolves submission title/DOI to its arXiv/OpenAlex OA PDF |
| 3. OA Aggregators | OpenAlex, Unpaywall, Semantic Scholar, Crossref, Europe PMC | Parallel metadata lookup yielding `best_oa_location`, `oa_locations[].url_for_pdf`, and `openAccessPdf` |
| 4. Shadow Libraries (skipped with `--oa-only`) | Sci-Hub (`sci-hub.ru` -> `sci-hub.vn`), Anna's Archive (`annas-archive.gd` / `.gl` / `.pk`) | Pure-HTTP SHA-256 Altcha PoW solver with 90-day session cache for Sci-Hub; Anna's Archive `/dyn/api/fast_download.json` (`ANNAS_ARCHIVE_KEY`), `/scidb/<doi>`, and `/md5/<md5>`[^shadow] |

[^download]: internal/paper/download.go
[^shadow]: internal/paper/shadow.go
