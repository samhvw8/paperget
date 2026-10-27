---
type: Architecture
title: Federated search and citation snowballing
description: Federated multi-engine search across five academic indexes with RRF and citation velocity ranking, and bidirectional OpenAlex citation snowballing.
resource: internal/paper/search.go
tags: [search, rrf, citation-velocity, snowball, openalex, epmc, arxiv, openreview, crossref]
status: stable
generated: { by: omp-agent/gemini-4-argon, at: '2026-10-27T00:00:00Z' }
sources:
  - id: search
    resource: internal/paper/search.go
    title: Federated search, canonical clustering, RRF ranking, and OpenAlex graph snowballing
---

# Federated Search Architecture

`paperhunt search` concurrently queries up to five academic search engines with a 4.5-second bounded deadline per engine:[^search]

1. **OpenAlex**: Full-text and metadata search with journal ID resolution and citation count/velocity metrics.
2. **Europe PMC**: Solr-indexed biomedical literature search supporting PMIDs, PMCIDs, clinical trial IDs, and publication year filters.
3. **arXiv**: Atom API search supporting abstract, title (`ti:`), and author queries with versioned identifiers.
4. **OpenReview**: Semantic `/notes/search` across ICLR, NeurIPS, ICML, and other ML venue submissions.
5. **Crossref**: Publisher-direct bibliographic search with DOI prefix matching and journal filtering.

# Deduplication and Ranking

Hits from all engines are clustered into canonical works by:
- Normalised DOI (case-insensitive, URL-stripped)
- Clean arXiv ID (version-stripped base)
- PubMed ID / PubMed Central ID
- OpenReview forum identifier
- Alphanumeric title key (spaces and punctuation stripped)

Clusters are ranked by **Reciprocal Rank Fusion (RRF)**:
$$RRF = \sum_{e \in 	ext{Engines}} rac{1}{60 + 	ext{Rank}(e)} 	imes (1 + 0.2 	imes \log_{10}(1 + 	ext{Velocity}))$$

where annual citation velocity is $rac{	ext{CitedBy}}{	ext{CurrentYear} - 	ext{PubYear} + 1}$.

# Citation Snowballing

`paperhunt snowball <id>` resolves the seed paper's OpenAlex work ID, then concurrently retrieves:
- **Backward references** (`referenced_works`): classic papers the seed paper built upon.
- **Forward citations** (`cites:<id>`): new research citing the seed paper, ranked by velocity or citations.

[^search]: internal/paper/search.go
