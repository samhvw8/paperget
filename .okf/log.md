# Update Log

## 2026-10-26

- Initialized `paperget` OKF bundle covering the multi-source PDF waterfall, Sci-Hub Altcha PoW solver, Anna's Archive integration, and replication bundle assembly.
- **2026-10-26** — `architecture/waterfall.md`: Added Elsevier / Cell Press / ScienceDirect / Lancet PII URL parsing (`Ref.PII`) and Crossref `alternative-id` DOI resolution (`resolveByPII`).

## 2026-10-27

- Renamed repository, module, binary, and OKF bundle from `paperget` to `paperhunt`.
- Added federated 5-engine academic search (`search.go`) across OpenAlex, Europe PMC, arXiv, OpenReview, and Crossref with RRF and citation velocity ranking.
- Added bidirectional OpenAlex citation snowballing (`paperhunt snowball`).
- Added title-to-preprint open-access fallback (`enrichPreprintByTitle`) to find author preprints before attempting shadow libraries.
- Added parallel worker pool (`-j N`) for batch downloads in `get`, `search -d`, and `snowball -d`.
- Excluded `10.48550/` (arXiv DOIs) from Sci-Hub queries to prevent invalid HTML responses.
- Added `.okf/architecture/search-and-snowball.md`.
- `ci.yml`, `release.yml`, and `.goreleaser.yaml`: Added GitHub Actions CI (`gofmt`, multi-OS `go vet`, `go test -race`, and 6-target cross-compilation artifact builds) plus tag-triggered GoReleaser releases.
