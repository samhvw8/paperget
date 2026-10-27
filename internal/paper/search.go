package paper

import (
	"context"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SearchOptions configures federated multi-engine paper search.
type SearchOptions struct {
	Query   string   `json:"query"`
	Sources []string `json:"sources,omitempty"` // openalex, epmc, arxiv, openreview, crossref (empty or "all" = default mix)
	Sort    string   `json:"sort,omitempty"`    // rrf (default), cited, velocity, date
	Year    string   `json:"year,omitempty"`    // e.g. "2024", "2020-2026", "2023-", "-2020"
	Journal string   `json:"journal,omitempty"` // journal/venue name filter
	OAOnly  bool     `json:"oa_only,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// SearchResult is a canonical deduplicated work returned by Search or Snowball.
type SearchResult struct {
	Title            string   `json:"title"`
	Authors          []string `json:"authors,omitempty"`
	Year             int      `json:"year,omitempty"`
	Date             string   `json:"date,omitempty"`
	Venue            string   `json:"venue,omitempty"`
	DOI              string   `json:"doi,omitempty"`
	ArXiv            string   `json:"arxiv,omitempty"`
	PMID             string   `json:"pmid,omitempty"`
	PMCID            string   `json:"pmcid,omitempty"`
	OpenReviewForum  string   `json:"openreview_forum,omitempty"`
	OpenReviewGroup  string   `json:"openreview_group,omitempty"`
	OpenAlexID       string   `json:"openalex_id,omitempty"`
	IsOA             bool     `json:"is_oa"`
	Retracted        bool     `json:"retracted,omitempty"`
	CitedBy          int      `json:"cited_by"`
	CitationVelocity float64  `json:"citation_velocity"`
	RRFScore         float64  `json:"rrf_score,omitempty"`
	PDFURL           string   `json:"pdf_url,omitempty"`
	LandingURL       string   `json:"landing_url,omitempty"`
	Sources          []string `json:"sources"`
	Direction        string   `json:"direction,omitempty"` // "ref", "cited", or "both" in snowball mode
}

// BestID returns the strongest identifier for passing to Parse / ResolveMeta / DownloadPDF.
func (sr SearchResult) BestID() string {
	if sr.DOI != "" {
		return sr.DOI
	}
	if sr.ArXiv != "" {
		return "arxiv:" + sr.ArXiv
	}
	if sr.PMCID != "" {
		return sr.PMCID
	}
	if sr.PMID != "" {
		return "pmid:" + sr.PMID
	}
	if sr.OpenReviewForum != "" {
		if sr.OpenReviewGroup != "" {
			return sr.OpenReviewForum + "@" + sr.OpenReviewGroup
		}
		return "https://openreview.net/forum?id=" + sr.OpenReviewForum
	}
	if sr.PDFURL != "" {
		return sr.PDFURL
	}
	return sr.Title
}

var reNonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeTitleKey(title string) string {
	low := strings.ToLower(strings.TrimSpace(title))
	return strings.Trim(reNonAlphaNum.ReplaceAllString(low, ""), " ")
}

// parseYearRange parses "2024", "2020-2026", "2022-", or "-2020" into (fromYear, toYear).
func parseYearRange(spec string) (int, int) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0
	}
	if !strings.Contains(spec, "-") {
		y, _ := strconv.Atoi(spec)
		return y, y
	}
	parts := strings.SplitN(spec, "-", 2)
	fromY, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	toY, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	return fromY, toY
}

type engineHit struct {
	engine string
	rank   int
	item   SearchResult
}

// Search runs a concurrent federated query across selected academic engines,
// deduplicates canonical works, and ranks them by RRF, citations, velocity, or date.
func (r *Resolver) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	q := strings.TrimSpace(opts.Query)
	if q == "" {
		return nil, fmt.Errorf("empty search query")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	perEngine := opts.Limit * 2
	if perEngine < 15 {
		perEngine = 15
	}
	if perEngine > 40 {
		perEngine = 40
	}

	enabled := map[string]bool{}
	for _, s := range opts.Sources {
		for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(s)), ",") {
			if part != "" && part != "all" {
				enabled[part] = true
			}
		}
	}
	useAll := len(enabled) == 0
	// If --journal is specified and no explicit engine was forced, focus on journal-aware engines (openalex, epmc, crossref).
	journalOnly := strings.TrimSpace(opts.Journal) != "" && useAll

	type engineFn struct {
		name string
		fn   func(context.Context, SearchOptions, int) ([]SearchResult, error)
	}
	var engines []engineFn
	if useAll || enabled["openalex"] {
		engines = append(engines, engineFn{"openalex", r.searchOpenAlex})
	}
	if useAll || enabled["epmc"] || enabled["pubmed"] {
		engines = append(engines, engineFn{"epmc", r.searchEuropePMC})
	}
	if (!journalOnly && useAll) || enabled["arxiv"] {
		engines = append(engines, engineFn{"arxiv", r.searchArXiv})
	}
	if (!journalOnly && useAll) || enabled["openreview"] {
		engines = append(engines, engineFn{"openreview", r.searchOpenReview})
	}
	if useAll || enabled["crossref"] {
		engines = append(engines, engineFn{"crossref", r.searchCrossref})
	}

	var (
		mu      sync.Mutex
		allHits []engineHit
		wg      sync.WaitGroup
	)
	for _, eng := range engines {
		eng := eng
		wg.Add(1)
		go func() {
			defer wg.Done()
			engCtx, cancel := context.WithTimeout(ctx, 4500*time.Millisecond)
			defer cancel()
			items, err := eng.fn(engCtx, opts, perEngine)
			if err != nil || len(items) == 0 {
				return
			}
			mu.Lock()
			for idx, it := range items {
				allHits = append(allHits, engineHit{
					engine: eng.name,
					rank:   idx + 1,
					item:   it,
				})
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	merged := mergeAndRankSearchResults(allHits, opts)
	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}
	return merged, nil
}

func mergeAndRankSearchResults(hits []engineHit, opts SearchOptions) []SearchResult {
	var clusters []*SearchResult
	doiIdx := map[string]int{}
	arxivIdx := map[string]int{}
	pmidIdx := map[string]int{}
	pmcidIdx := map[string]int{}
	orIdx := map[string]int{}
	titleIdx := map[string]int{}

	fromY, toY := parseYearRange(opts.Year)
	journalLow := strings.ToLower(strings.TrimSpace(opts.Journal))
	nowYear := time.Now().Year()

	for _, h := range hits {
		it := h.item
		it.Title = strings.TrimSpace(it.Title)
		if it.Title == "" || strings.EqualFold(it.Title, "Login") {
			continue
		}
		it.DOI = cleanDOI(it.DOI)
		if m := reArXivDOI.FindStringSubmatch(it.DOI); m != nil && it.ArXiv == "" {
			it.ArXiv = ArXivBase(m[1])
		}
		if it.ArXiv != "" {
			it.ArXiv = ArXivBase(it.ArXiv)
		}
		tKey := normalizeTitleKey(it.Title)

		idx := -1
		if it.DOI != "" {
			if i, ok := doiIdx[it.DOI]; ok {
				idx = i
			}
		}
		if idx == -1 && it.ArXiv != "" {
			if i, ok := arxivIdx[it.ArXiv]; ok {
				idx = i
			}
		}
		if idx == -1 && it.PMID != "" {
			if i, ok := pmidIdx[it.PMID]; ok {
				idx = i
			}
		}
		if idx == -1 && it.PMCID != "" {
			if i, ok := pmcidIdx[it.PMCID]; ok {
				idx = i
			}
		}
		if idx == -1 && it.OpenReviewForum != "" {
			if i, ok := orIdx[it.OpenReviewForum]; ok {
				idx = i
			}
		}
		if idx == -1 && len(tKey) >= 16 {
			if i, ok := titleIdx[tKey]; ok {
				idx = i
			}
		}

		rrfDelta := 1.0 / float64(60+h.rank)
		if idx == -1 {
			cp := it
			cp.Sources = []string{h.engine}
			cp.RRFScore = rrfDelta
			idx = len(clusters)
			clusters = append(clusters, &cp)
		} else {
			dst := clusters[idx]
			dst.RRFScore += rrfDelta
			if !containsStr(dst.Sources, h.engine) {
				dst.Sources = append(dst.Sources, h.engine)
			}
			if len(it.Title) > len(dst.Title) && len(dst.Title) < 24 {
				dst.Title = it.Title
			}
			if len(dst.Authors) == 0 && len(it.Authors) > 0 {
				dst.Authors = it.Authors
			}
			if dst.Year == 0 && it.Year > 0 {
				dst.Year = it.Year
			}
			if dst.Date == "" && it.Date != "" {
				dst.Date = it.Date
			}
			if dst.Venue == "" && it.Venue != "" {
				dst.Venue = it.Venue
			}
			if dst.DOI == "" && it.DOI != "" {
				dst.DOI = it.DOI
			}
			if dst.ArXiv == "" && it.ArXiv != "" {
				dst.ArXiv = it.ArXiv
			}
			if dst.PMID == "" && it.PMID != "" {
				dst.PMID = it.PMID
			}
			if dst.PMCID == "" && it.PMCID != "" {
				dst.PMCID = it.PMCID
			}
			if dst.OpenReviewForum == "" && it.OpenReviewForum != "" {
				dst.OpenReviewForum = it.OpenReviewForum
			}
			if dst.OpenReviewGroup == "" && it.OpenReviewGroup != "" {
				dst.OpenReviewGroup = it.OpenReviewGroup
			}
			if dst.OpenAlexID == "" && it.OpenAlexID != "" {
				dst.OpenAlexID = it.OpenAlexID
			}
			if it.IsOA {
				dst.IsOA = true
			}
			if it.Retracted {
				dst.Retracted = true
			}
			if it.CitedBy > dst.CitedBy {
				dst.CitedBy = it.CitedBy
			}
			if dst.PDFURL == "" && it.PDFURL != "" {
				dst.PDFURL = it.PDFURL
			}
			if dst.LandingURL == "" && it.LandingURL != "" {
				dst.LandingURL = it.LandingURL
			}
		}

		c := clusters[idx]
		if c.DOI != "" {
			doiIdx[c.DOI] = idx
		}
		if c.ArXiv != "" {
			arxivIdx[c.ArXiv] = idx
		}
		if c.PMID != "" {
			pmidIdx[c.PMID] = idx
		}
		if c.PMCID != "" {
			pmcidIdx[c.PMCID] = idx
		}
		if c.OpenReviewForum != "" {
			orIdx[c.OpenReviewForum] = idx
		}
		if len(tKey) >= 16 {
			titleIdx[tKey] = idx
		}
	}

	var out []SearchResult
	for _, c := range clusters {
		if fromY > 0 && c.Year > 0 && c.Year < fromY {
			continue
		}
		if toY > 0 && c.Year > 0 && c.Year > toY {
			continue
		}
		if opts.OAOnly && !c.IsOA && c.ArXiv == "" && c.PMCID == "" && c.PDFURL == "" {
			continue
		}
		if journalLow != "" && c.Venue != "" && !strings.Contains(strings.ToLower(c.Venue), journalLow) && !containsStr(c.Sources, "openalex") {
			continue
		}
		age := 1
		if c.Year > 0 && nowYear >= c.Year {
			age = nowYear - c.Year + 1
		}
		c.CitationVelocity = math.Round((float64(c.CitedBy)/float64(age))*10) / 10
		c.RRFScore = math.Round(c.RRFScore*10000) / 10000
		out = append(out, *c)
	}

	sortMode := strings.ToLower(strings.TrimSpace(opts.Sort))
	sort.SliceStable(out, func(i, j int) bool {
		switch sortMode {
		case "cited", "citations":
			if out[i].CitedBy != out[j].CitedBy {
				return out[i].CitedBy > out[j].CitedBy
			}
			return out[i].RRFScore > out[j].RRFScore
		case "velocity":
			if out[i].CitationVelocity != out[j].CitationVelocity {
				return out[i].CitationVelocity > out[j].CitationVelocity
			}
			return out[i].CitedBy > out[j].CitedBy
		case "date", "year", "newest":
			if out[i].Date != out[j].Date && out[i].Date != "" && out[j].Date != "" {
				return out[i].Date > out[j].Date
			}
			if out[i].Year != out[j].Year {
				return out[i].Year > out[j].Year
			}
			return out[i].RRFScore > out[j].RRFScore
		default: // "rrf" / "relevance"
			scoreI := out[i].RRFScore * (1.0 + 0.15*math.Log1p(out[i].CitationVelocity))
			scoreJ := out[j].RRFScore * (1.0 + 0.15*math.Log1p(out[j].CitationVelocity))
			return scoreI > scoreJ
		}
	})
	return out
}

func containsStr(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// resolveOpenAlexSourceID looks up a journal/venue display name in OpenAlex /sources.
func (r *Resolver) resolveOpenAlexSourceID(ctx context.Context, journal string) string {
	u := fmt.Sprintf("%s/sources?search=%s&per-page=5", strings.TrimRight(r.OpenAlexBase, "/"), url.QueryEscape(journal))
	var resp struct {
		Results []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"results"`
	}
	if err := r.getJSON(ctx, u, nil, &resp); err != nil || len(resp.Results) == 0 {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(journal))
	for _, s := range resp.Results {
		if strings.EqualFold(strings.TrimSpace(s.DisplayName), want) {
			parts := strings.Split(s.ID, "/")
			return parts[len(parts)-1]
		}
	}
	parts := strings.Split(resp.Results[0].ID, "/")
	return parts[len(parts)-1]
}

func (r *Resolver) searchOpenAlex(ctx context.Context, opts SearchOptions, limit int) ([]SearchResult, error) {
	var filters []string
	fromY, toY := parseYearRange(opts.Year)
	if fromY > 0 {
		filters = append(filters, fmt.Sprintf("from_publication_date:%04d-01-01", fromY))
	}
	if toY > 0 {
		filters = append(filters, fmt.Sprintf("to_publication_date:%04d-12-31", toY))
	}
	if opts.OAOnly {
		filters = append(filters, "is_oa:true")
	}
	if strings.TrimSpace(opts.Journal) != "" {
		if srcID := r.resolveOpenAlexSourceID(ctx, opts.Journal); srcID != "" {
			filters = append(filters, "primary_location.source.id:"+srcID)
		}
	}

	params := url.Values{}
	params.Set("search", opts.Query)
	params.Set("per-page", strconv.Itoa(limit))
	if len(filters) > 0 {
		params.Set("filter", strings.Join(filters, ","))
	}
	switch strings.ToLower(strings.TrimSpace(opts.Sort)) {
	case "cited", "citations", "velocity":
		params.Set("sort", "cited_by_count:desc")
	case "date", "year", "newest":
		params.Set("sort", "publication_date:desc")
	}

	u := fmt.Sprintf("%s/works?%s", strings.TrimRight(r.OpenAlexBase, "/"), params.Encode())
	var resp struct {
		Results []openAlexWork `json:"results"`
	}
	if err := r.getJSON(ctx, u, nil, &resp); err != nil {
		return nil, err
	}
	return convertOpenAlexWorks(resp.Results), nil
}

type openAlexWork struct {
	ID              string `json:"id"`
	DOI             string `json:"doi"`
	Title           string `json:"title"`
	DisplayName     string `json:"display_name"`
	PublicationYear int    `json:"publication_year"`
	PublicationDate string `json:"publication_date"`
	CitedByCount    int    `json:"cited_by_count"`
	IsRetracted     bool   `json:"is_retracted"`
	IDs             struct {
		PMID  string `json:"pmid"`
		PMCID string `json:"pmcid"`
	} `json:"ids"`
	OpenAccess struct {
		IsOA  bool   `json:"is_oa"`
		OAURL string `json:"oa_url"`
	} `json:"open_access"`
	BestOALocation *struct {
		PDFURL     string `json:"pdf_url"`
		LandingURL string `json:"landing_page_url"`
	} `json:"best_oa_location"`
	PrimaryLocation *struct {
		PDFURL     string `json:"pdf_url"`
		LandingURL string `json:"landing_page_url"`
		Source     *struct {
			DisplayName string `json:"display_name"`
		} `json:"source"`
	} `json:"primary_location"`
	Authorships []struct {
		Author struct {
			DisplayName string `json:"display_name"`
		} `json:"author"`
	} `json:"authorships"`
}

func convertOpenAlexWorks(works []openAlexWork) []SearchResult {
	var out []SearchResult
	for _, w := range works {
		title := strings.TrimSpace(w.Title)
		if title == "" {
			title = strings.TrimSpace(w.DisplayName)
		}
		if title == "" {
			continue
		}
		sr := SearchResult{
			Title:      title,
			Year:       w.PublicationYear,
			Date:       w.PublicationDate,
			DOI:        cleanDOI(w.DOI),
			PMID:       strings.TrimPrefix(w.IDs.PMID, "https://pubmed.ncbi.nlm.nih.gov/"),
			PMCID:      strings.TrimPrefix(w.IDs.PMCID, "https://www.ncbi.nlm.nih.gov/pmc/articles/"),
			OpenAlexID: strings.TrimPrefix(w.ID, "https://openalex.org/"),
			IsOA:       w.OpenAccess.IsOA,
			Retracted:  w.IsRetracted,
			CitedBy:    w.CitedByCount,
			PDFURL:     w.OpenAccess.OAURL,
		}
		if w.BestOALocation != nil {
			if w.BestOALocation.PDFURL != "" {
				sr.PDFURL = w.BestOALocation.PDFURL
			}
			sr.LandingURL = w.BestOALocation.LandingURL
		}
		if w.PrimaryLocation != nil {
			if sr.LandingURL == "" {
				sr.LandingURL = w.PrimaryLocation.LandingURL
			}
			if w.PrimaryLocation.Source != nil {
				sr.Venue = w.PrimaryLocation.Source.DisplayName
			}
		}
		for _, a := range w.Authorships {
			if name := strings.TrimSpace(a.Author.DisplayName); name != "" {
				sr.Authors = append(sr.Authors, name)
			}
		}
		out = append(out, sr)
	}
	return out
}

func (r *Resolver) searchEuropePMC(ctx context.Context, opts SearchOptions, limit int) ([]SearchResult, error) {
	q := opts.Query
	fromY, toY := parseYearRange(opts.Year)
	if fromY > 0 && toY > 0 {
		q += fmt.Sprintf(" AND (PUB_YEAR:[%d TO %d])", fromY, toY)
	} else if fromY > 0 {
		q += fmt.Sprintf(" AND (PUB_YEAR:[%d TO 2099])", fromY)
	}
	if opts.OAOnly {
		q += " AND (OPEN_ACCESS:y)"
	}
	if strings.TrimSpace(opts.Journal) != "" {
		q += fmt.Sprintf(` AND (JOURNAL:"%s")`, strings.TrimSpace(opts.Journal))
	}

	params := url.Values{}
	params.Set("query", q)
	params.Set("format", "json")
	params.Set("pageSize", strconv.Itoa(limit))
	if strings.EqualFold(opts.Sort, "cited") || strings.EqualFold(opts.Sort, "velocity") {
		params.Set("sort", "CITED desc")
	}

	u := fmt.Sprintf("%s/search?%s", strings.TrimRight(r.EPMCBase, "/"), params.Encode())
	var resp struct {
		ResultList struct {
			Result []struct {
				Title        string `json:"title"`
				AuthorString string `json:"authorString"`
				PubYear      string `json:"pubYear"`
				JournalTitle string `json:"journalTitle"`
				DOI          string `json:"doi"`
				PMID         string `json:"pmid"`
				PMCID        string `json:"pmcid"`
				IsOpenAccess string `json:"isOpenAccess"`
				CitedByCount int    `json:"citedByCount"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if err := r.getJSON(ctx, u, nil, &resp); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, it := range resp.ResultList.Result {
		yr, _ := strconv.Atoi(it.PubYear)
		var authors []string
		for _, a := range strings.Split(it.AuthorString, ",") {
			if a = strings.TrimSpace(strings.TrimSuffix(a, ".")); a != "" {
				authors = append(authors, a)
			}
		}
		out = append(out, SearchResult{
			Title:   strings.TrimSuffix(strings.TrimSpace(it.Title), "."),
			Authors: authors,
			Year:    yr,
			Venue:   it.JournalTitle,
			DOI:     cleanDOI(it.DOI),
			PMID:    it.PMID,
			PMCID:   it.PMCID,
			IsOA:    it.IsOpenAccess == "Y" || it.PMCID != "",
			CitedBy: it.CitedByCount,
		})
	}
	return out, nil
}

func (r *Resolver) searchArXiv(ctx context.Context, opts SearchOptions, limit int) ([]SearchResult, error) {
	u := fmt.Sprintf("%s?search_query=all:%s&max_results=%d", r.ArXivAPIBase, url.QueryEscape(opts.Query), limit)
	if strings.EqualFold(opts.Sort, "date") {
		u += "&sortBy=submittedDate&sortOrder=descending"
	}
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	var feed struct {
		Entries []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Published string `xml:"published"`
			DOI       string `xml:"doi"`
			Authors   []struct {
				Name string `xml:"name"`
			} `xml:"author"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, e := range feed.Entries {
		id := e.ID
		if idx := strings.Index(id, "/abs/"); idx != -1 {
			id = id[idx+5:]
		}
		id = ArXivBase(strings.TrimSpace(id))
		if !IsArXivID(id) {
			continue
		}
		yr := 0
		date := ""
		if len(e.Published) >= 10 {
			date = e.Published[:10]
			yr, _ = strconv.Atoi(e.Published[:4])
		}
		var authors []string
		for _, a := range e.Authors {
			if n := strings.TrimSpace(a.Name); n != "" {
				authors = append(authors, n)
			}
		}
		out = append(out, SearchResult{
			Title:      collapseWhitespace(e.Title),
			Authors:    authors,
			Year:       yr,
			Date:       date,
			Venue:      "arXiv",
			DOI:        cleanDOI(e.DOI),
			ArXiv:      id,
			IsOA:       true,
			PDFURL:     "https://arxiv.org/pdf/" + id + ".pdf",
			LandingURL: "https://arxiv.org/abs/" + id,
		})
	}
	return out, nil
}

func cleanDOI(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://doi.org/")
	s = strings.TrimPrefix(s, "http://doi.org/")
	s = strings.TrimPrefix(s, "https://dx.doi.org/")
	s = strings.TrimPrefix(s, "doi:")
	s = strings.TrimPrefix(s, "DOI:")
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), ".,;)"))
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func extractORGroup(n openReviewNote) string {
	if n.Domain != "" && n.Number > 0 {
		return fmt.Sprintf("%s/Submission%d/", strings.TrimSuffix(n.Domain, "/"), n.Number)
	}
	for _, sig := range n.Signatures {
		if idx := strings.Index(sig, "/Submission"); idx != -1 {
			rest := sig[idx+1:]
			if slash := strings.Index(rest, "/"); slash != -1 {
				return sig[:idx+1+slash+1]
			}
			return sig + "/"
		}
		if idx := strings.Index(sig, "/Paper"); idx != -1 {
			rest := sig[idx+1:]
			if slash := strings.Index(rest, "/"); slash != -1 {
				return sig[:idx+1+slash+1]
			}
			return sig + "/"
		}
	}
	return ""
}

func (r *Resolver) searchOpenReview(ctx context.Context, opts SearchOptions, limit int) ([]SearchResult, error) {
	u := fmt.Sprintf("%s/notes/search?term=%s&content=all&source=forum&limit=%d",
		strings.TrimRight(r.OpenReviewV2, "/"), url.QueryEscape(opts.Query), limit)
	var resp struct {
		Notes []openReviewNote `json:"notes"`
	}
	if err := r.getJSON(ctx, u, openReviewAuthHeaders(), &resp); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, n := range resp.Notes {
		if n.Replyto != "" && n.ID != n.Forum {
			continue
		}
		title := orStr(n.Content, "title")
		if title == "" {
			continue
		}
		yr := 0
		date := ""
		if n.Cdate > 0 {
			tm := time.UnixMilli(n.Cdate).UTC()
			yr = tm.Year()
			date = tm.Format("2006-01-02")
		}
		venue := orStr(n.Content, "venue")
		if venue == "" {
			venue = orStr(n.Content, "venueid")
		}
		if venue == "" {
			venue = "OpenReview"
		}
		grp := extractORGroup(n)
		out = append(out, SearchResult{
			Title:           title,
			Authors:         orSlice(n.Content, "authors"),
			Year:            yr,
			Date:            date,
			Venue:           venue,
			OpenReviewForum: n.Forum,
			OpenReviewGroup: grp,
			IsOA:            true,
			PDFURL:          fmt.Sprintf("%s/pdf?id=%s", strings.TrimRight(r.OpenReviewV2, "/"), n.Forum),
			LandingURL:      "https://openreview.net/forum?id=" + n.Forum,
		})
	}
	return out, nil
}

func (r *Resolver) searchCrossref(ctx context.Context, opts SearchOptions, limit int) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("query", opts.Query)
	params.Set("rows", strconv.Itoa(limit))
	var filters []string
	fromY, toY := parseYearRange(opts.Year)
	if fromY > 0 {
		filters = append(filters, fmt.Sprintf("from-pub-date:%04d", fromY))
	}
	if toY > 0 {
		filters = append(filters, fmt.Sprintf("until-pub-date:%04d", toY))
	}
	if strings.TrimSpace(opts.Journal) != "" {
		params.Set("query.container-title", strings.TrimSpace(opts.Journal))
	}
	if len(filters) > 0 {
		params.Set("filter", strings.Join(filters, ","))
	}
	u := "https://api.crossref.org/works?" + params.Encode()
	var resp struct {
		Message struct {
			Items []struct {
				DOI            string   `json:"DOI"`
				Title          []string `json:"title"`
				ContainerTitle []string `json:"container-title"`
				ReferenceCount int      `json:"is-referenced-by-count"`
				Author         []struct {
					Given  string `json:"given"`
					Family string `json:"family"`
				} `json:"author"`
				Published struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := r.getJSON(ctx, u, nil, &resp); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, it := range resp.Message.Items {
		if len(it.Title) == 0 || strings.TrimSpace(it.Title[0]) == "" {
			continue
		}
		yr := 0
		if len(it.Published.DateParts) > 0 && len(it.Published.DateParts[0]) > 0 {
			yr = it.Published.DateParts[0][0]
		}
		venue := ""
		if len(it.ContainerTitle) > 0 {
			venue = it.ContainerTitle[0]
		}
		var authors []string
		for _, a := range it.Author {
			full := strings.TrimSpace(a.Given + " " + a.Family)
			if full != "" {
				authors = append(authors, full)
			}
		}
		out = append(out, SearchResult{
			Title:   collapseWhitespace(it.Title[0]),
			Authors: authors,
			Year:    yr,
			Venue:   venue,
			DOI:     cleanDOI(it.DOI),
			CitedBy: it.ReferenceCount,
		})
	}
	return out, nil
}

// SnowballOptions configures backward-reference and forward-citation graph traversal.
type SnowballOptions struct {
	Direction string `json:"direction,omitempty"` // "both" (default), "refs", "cited"
	Sort      string `json:"sort,omitempty"`      // "cited" (default), "velocity", "date"
	OAOnly    bool   `json:"oa_only,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// SnowballResult holds the resolved seed work and its ranked backward/forward citation neighbors.
type SnowballResult struct {
	Seed       *Meta          `json:"seed"`
	OpenAlexID string         `json:"openalex_id"`
	Items      []SearchResult `json:"items"`
}

// Snowball resolves a seed paper and traverses its backward references and/or forward citations via OpenAlex.
func (r *Resolver) Snowball(ctx context.Context, rawSeed string, opts SnowballOptions) (*SnowballResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Sort == "" {
		opts.Sort = "cited"
	}
	dir := strings.ToLower(strings.TrimSpace(opts.Direction))
	if dir == "" {
		dir = "both"
	}

	ref := Parse(rawSeed)
	seedMeta, err := r.ResolveMeta(ctx, ref)
	if err != nil {
		return nil, err
	}

	oaShort := strings.TrimPrefix(seedMeta.OpenAlexID, "https://openalex.org/")
	refWorks := seedMeta.ReferencedWorks
	if oaShort == "" && seedMeta.Title != "" {
		fallbackURL := fmt.Sprintf("%s/works?search=%s&per-page=1", strings.TrimRight(r.OpenAlexBase, "/"), url.QueryEscape(seedMeta.Title))
		var seedWork struct {
			Results []struct {
				ID              string   `json:"id"`
				ReferencedWorks []string `json:"referenced_works"`
			} `json:"results"`
		}
		if err := r.getJSON(ctx, fallbackURL, nil, &seedWork); err == nil && len(seedWork.Results) > 0 {
			oaShort = strings.TrimPrefix(seedWork.Results[0].ID, "https://openalex.org/")
			refWorks = seedWork.Results[0].ReferencedWorks
		}
	}
	if oaShort == "" {
		return nil, fmt.Errorf("could not locate OpenAlex work ID for seed %q (%s)", rawSeed, seedMeta.Title)
	}

	var (
		mu      sync.Mutex
		byKey   = map[string]*SearchResult{}
		wg      sync.WaitGroup
		nowYear = time.Now().Year()
	)

	addItems := func(items []SearchResult, label string) {
		mu.Lock()
		defer mu.Unlock()
		for _, it := range items {
			key := it.OpenAlexID
			if key == "" {
				key = it.BestID()
			}
			if existing, ok := byKey[key]; ok {
				if existing.Direction != label {
					existing.Direction = "both"
				}
				continue
			}
			cp := it
			cp.Direction = label
			cp.Sources = []string{"openalex-" + label}
			age := 1
			if cp.Year > 0 && nowYear >= cp.Year {
				age = nowYear - cp.Year + 1
			}
			cp.CitationVelocity = math.Round((float64(cp.CitedBy)/float64(age))*10) / 10
			byKey[key] = &cp
		}
	}

	// 1. Backward references (papers cited BY the seed paper)
	if (dir == "both" || dir == "refs" || dir == "backward") && len(refWorks) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ids []string
			for _, rw := range refWorks {
				short := strings.TrimPrefix(rw, "https://openalex.org/")
				if short != "" {
					ids = append(ids, short)
				}
				if len(ids) >= 45 {
					break
				}
			}
			if len(ids) == 0 {
				return
			}
			filter := "openalex:" + strings.Join(ids, "|")
			if opts.OAOnly {
				filter += ",is_oa:true"
			}
			u := fmt.Sprintf("%s/works?filter=%s&sort=cited_by_count:desc&per-page=%d",
				strings.TrimRight(r.OpenAlexBase, "/"), url.QueryEscape(filter), opts.Limit*2)
			var resp struct {
				Results []openAlexWork `json:"results"`
			}
			if err := r.getJSON(ctx, u, nil, &resp); err == nil {
				addItems(convertOpenAlexWorks(resp.Results), "ref")
			}
		}()
	}

	// 2. Forward citations (papers that CITE the seed paper)
	if dir == "both" || dir == "cited" || dir == "forward" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filter := "cites:" + oaShort
			if opts.OAOnly {
				filter += ",is_oa:true"
			}
			sortParam := "cited_by_count:desc"
			if strings.EqualFold(opts.Sort, "date") {
				sortParam = "publication_date:desc"
			}
			u := fmt.Sprintf("%s/works?filter=%s&sort=%s&per-page=%d",
				strings.TrimRight(r.OpenAlexBase, "/"), url.QueryEscape(filter), sortParam, opts.Limit*2)
			var resp struct {
				Results []openAlexWork `json:"results"`
			}
			if err := r.getJSON(ctx, u, nil, &resp); err == nil {
				addItems(convertOpenAlexWorks(resp.Results), "cited")
			}
		}()
	}
	wg.Wait()

	var items []SearchResult
	for _, v := range byKey {
		items = append(items, *v)
	}
	sortMode := strings.ToLower(strings.TrimSpace(opts.Sort))
	sort.SliceStable(items, func(i, j int) bool {
		switch sortMode {
		case "velocity":
			if items[i].CitationVelocity != items[j].CitationVelocity {
				return items[i].CitationVelocity > items[j].CitationVelocity
			}
			return items[i].CitedBy > items[j].CitedBy
		case "date", "year", "newest":
			if items[i].Year != items[j].Year {
				return items[i].Year > items[j].Year
			}
			return items[i].CitedBy > items[j].CitedBy
		default:
			return items[i].CitedBy > items[j].CitedBy
		}
	})
	if len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	return &SnowballResult{
		Seed:       seedMeta,
		OpenAlexID: oaShort,
		Items:      items,
	}, nil
}
