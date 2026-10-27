---
type: System Overview
title: paperhunt overview
description: Academic paper hunting (federated search, citation snowballing, RRF ranking), parallel downloading, and replication bundle builder covering open-access repositories and shadow-library fallbacks.
resource: cmd/paperhunt/main.go
tags: [papers, cli, search, snowball, open-access, arxiv, biorxiv, openreview, annas-archive]
status: stable
generated: { by: omp-agent/gemini-4-argon, at: '2026-10-27T00:00:00Z' }
sources:
  - id: cli
    resource: cmd/paperhunt/main.go
    title: CLI entrypoint and subcommands
---

# Purpose

`paperhunt` searches, ranks, snowballs, downloads, and bundles academic research to local disk. While `Parallax` (`~/workspace/parallax`) reads open-access full text into markdown for context-cheap AI judgment and strictly excludes shadow libraries, `paperhunt` runs locally as a compiled CLI to discover papers across 5 academic engines, traverse citation graphs, and fetch raw PDFs, LaTeX sources, supplements, peer reviews, and code repositories across both legal open-access sources and shadow-library fallbacks.[^cli]

# Commands

| Command | Behavior |
|---|---|
| `paperhunt search [flags] <query>` | Federated search across OpenAlex, Europe PMC, arXiv, OpenReview, Crossref with RRF + velocity ranking and optional parallel PDF download (`-d`) |
| `paperhunt snowball [flags] <id>` | Bidirectional citation graph traversal (backward references + forward citations) around a seed paper |
| `paperhunt [get] [flags] <id...>` | Resolve metadata and download verified `.pdf`s in parallel (`-j N`, default 4) with optional `.json`, `.bib`, `.reviews.md` sidecars |
| `paperhunt bundle [flags] <id>` | Create a self-contained replication folder with `paper.pdf`, `tex/`, `reviews.md`, `pmc/`, and `--clone` for code repos |
| `paperhunt info [flags] <id>` | Resolve metadata, retraction status, OA/shadow PDF candidates, OpenReview forum, and linked code repos without downloading |

[^cli]: cmd/paperhunt/main.go
