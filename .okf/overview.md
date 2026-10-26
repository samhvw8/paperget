---
type: System Overview
title: paperget overview
description: Local academic paper downloader and replication bundle builder covering open-access repositories, preprint servers, OpenReview, and shadow-library fallbacks.
resource: cmd/paperget/main.go
tags: [papers, cli, open-access, arxiv, biorxiv, openreview, annas-archive]
status: stable
generated: { by: omp-agent/gemini-4-argon, at: '2026-10-26T15:00:00Z' }
sources:
  - id: cli
    resource: cmd/paperget/main.go
    title: CLI entrypoint and subcommands
---

# Purpose

`paperget` downloads academic papers and replication artifacts to local disk. While `Parallax` (`~/workspace/parallax`) reads open-access full text into markdown for context-cheap AI judgment and strictly excludes shadow libraries, `paperget` runs locally to fetch raw PDFs, LaTeX sources, supplements, peer reviews, and code repositories across both legal open-access sources and shadow-library fallbacks.[^cli]

# Commands

| Command | Behavior |
|---|---|
| `paperget [get] <id-or-url-or-query>...` | Resolve metadata and download verified `.pdf` (plus optional `--meta`, `--bib`, `--reviews`) |
| `paperget bundle <id-or-url-or-query>` | Create a self-contained folder with `paper.pdf`, `paper.json`, `paper.bib`, `tex/` (arXiv e-print source), `reviews.md` (OpenReview), `pmc/` (JATS + supplements), and `code-repos.json` (plus `--clone` into `code/`) |
| `paperget info <id-or-url-or-query>` | Resolve metadata, retraction status, OA/shadow PDF candidates, OpenReview forum, and linked code repos without downloading the PDF |

[^cli]: cmd/paperget/main.go
