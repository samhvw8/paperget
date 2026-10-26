---
type: Architecture
title: Replication bundles — TeX, reviews, supplements, and code
description: How paperget bundle assembles a complete paper directory for deep reading or code replication.
resource: internal/paper/bundle.go
tags: [bundle, replicate, latex, openreview, github, huggingface]
status: stable
generated: { by: omp-agent/gemini-4-argon, at: '2026-10-26T15:00:00Z' }
sources:
  - id: bundle
    resource: internal/paper/bundle.go
    title: Bundle and replication artifact extractor
---

# Bundle Contents

`paperget bundle` (or `paperget replicate`) creates a deterministic directory holding every public artifact around a paper:[^bundle]

1. `paper.pdf` — Verified PDF from the waterfall.
2. `paper.json` & `paper.bib` — Cross-referenced metadata (retraction status, IDs, citations, venue) and BibTeX.
3. `tex/` — Extracted arXiv `e-print` (`https://arxiv.org/e-print/<id>`) LaTeX source files (`.tex`, `.bbl`, figures) for exact equation and hyperparameter inspection.
4. `reviews.md` — Public peer reviews, author rebuttals, and decision notes from OpenReview (`api2.openreview.net` and `api.openreview.net`).
5. `pmc/` — JATS XML (`.xml`) and supplementary files from the `pmc-oa-opendata` S3 bucket when a PMCID is present.
6. `code-repos.json` (and `code/` with `--clone`) — Code and model repositories discovered from Hugging Face Papers (`huggingface.co/api/papers/<id>`), OpenReview `code` fields, abstract/comment text, and extracted `.tex` macros.

[^bundle]: internal/paper/bundle.go
