package paper

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// PDFCandidate is one potential PDF download route in the waterfall.
type PDFCandidate struct {
	URL      string            `json:"url"`
	Source   string            `json:"source"`
	Priority int               `json:"priority"` // lower runs first
	Headers  map[string]string `json:"headers,omitempty"`
}

// Meta holds cross-resolved metadata and discovered download candidates for a paper.
type Meta struct {
	Ref               Ref              `json:"ref"`
	Title             string           `json:"title"`
	Authors           []string         `json:"authors,omitempty"`
	Year              string           `json:"year,omitempty"`
	Venue             string           `json:"venue,omitempty"`
	Type              string           `json:"type,omitempty"`
	Abstract          string           `json:"abstract,omitempty"`
	DOI               string           `json:"doi,omitempty"`
	ArXiv             string           `json:"arxiv,omitempty"`
	PMID              string           `json:"pmid,omitempty"`
	PMCID             string           `json:"pmcid,omitempty"`
	OpenReviewForum   string           `json:"openreview_forum,omitempty"`
	OpenReviewGroup   string           `json:"openreview_group,omitempty"`
	OpenAlexID        string           `json:"openalex_id,omitempty"`
	ReferencedWorks   []string         `json:"referenced_works,omitempty"`
	PublishedDOI      string           `json:"published_doi,omitempty"`
	IsOA              bool             `json:"is_oa"`
	Retracted         bool             `json:"retracted"`
	RetractionNotices []string         `json:"retraction_notices,omitempty"`
	CitedBy           int              `json:"cited_by,omitempty"`
	License           string           `json:"license,omitempty"`
	PDFCandidates     []PDFCandidate   `json:"pdf_candidates,omitempty"`
	CodeRepos         []string         `json:"code_repos,omitempty"`
	SavedPDFPath      string           `json:"saved_pdf_path,omitempty"`
	SavedPDFSource    string           `json:"saved_pdf_source,omitempty"`
	SavedPDFSHA256    string           `json:"saved_pdf_sha256,omitempty"`
	SavedPDFBytes     int64            `json:"saved_pdf_bytes,omitempty"`
	orNotes           []openReviewNote `json:"-"`
}

// Resolver configures HTTP endpoints and timeouts for metadata and PDF resolution.
type Resolver struct {
	Client         *http.Client
	OpenAlexBase   string
	EPMCBase       string
	ArXivAPIBase   string
	BioRxivAPIBase string
	UnpaywallBase  string
	UnpaywallEmail string
	OpenReviewV2   string
	OpenReviewV1   string
	PMCS3Base      string
	ZenodoAPIBase  string
	HFPapersBase   string
}

// NewResolver returns a Resolver wired to live public endpoints.
func NewResolver() *Resolver {
	email := os.Getenv("UNPAYWALL_EMAIL")
	if email == "" {
		email = "paperhunt@openresearch.org"
	}
	return &Resolver{
		Client: &http.Client{
			Timeout: 15 * time.Second,
		},
		OpenAlexBase:   "https://api.openalex.org",
		EPMCBase:       "https://www.ebi.ac.uk/europepmc/webservices/rest",
		ArXivAPIBase:   "https://export.arxiv.org/api/query",
		BioRxivAPIBase: "https://api.biorxiv.org/details",
		UnpaywallBase:  "https://api.unpaywall.org/v2",
		UnpaywallEmail: email,
		OpenReviewV2:   "https://api2.openreview.net",
		OpenReviewV1:   "https://api.openreview.net",
		PMCS3Base:      "https://pmc-oa-opendata.s3.amazonaws.com",
		ZenodoAPIBase:  "https://zenodo.org/api/records",
		HFPapersBase:   "https://huggingface.co/api/papers",
	}
}

func (r *Resolver) getJSON(ctx context.Context, rawURL string, hdrs map[string]string, dst any) error {
	body, err := r.getBytes(ctx, rawURL, hdrs)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func (r *Resolver) getBytes(ctx context.Context, rawURL string, hdrs map[string]string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// ResolveMeta cross-resolves identifiers, metadata, OA PDF candidates, and code repos.
func (r *Resolver) ResolveMeta(ctx context.Context, ref Ref) (*Meta, error) {
	m := &Meta{
		Ref:             ref,
		DOI:             ref.DOI,
		ArXiv:           ref.ArXiv,
		PMID:            ref.PMID,
		PMCID:           ref.PMCID,
		OpenReviewForum: ref.OpenReviewForum,
		OpenReviewGroup: ref.OpenReviewGroup,
	}
	if ref.DirectPDF != "" {
		m.addCandidate(ref.DirectPDF, "direct-url", 5, nil)
	}

	if m.DOI == "" && ref.PII != "" {
		_ = r.resolveByPII(ctx, m, ref.PII)
	}

	if m.DOI == "" && m.ArXiv == "" && m.PMID == "" && m.PMCID == "" && m.OpenReviewForum == "" && ref.Query != "" {
		r.resolveQuery(ctx, m, ref.Query)
	}
	if m.OpenReviewForum != "" {
		r.enrichFromOpenReview(ctx, m)
		if m.ArXiv == "" && m.DOI == "" && m.Title != "" {
			r.resolveByTitle(ctx, m, m.Title)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	if m.ArXiv != "" {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.enrichFromArXiv(ctx, &mu, m, id)
		}(m.ArXiv)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.enrichFromHFPapers(ctx, &mu, m, ArXivBase(id))
		}(m.ArXiv)
	}

	if m.DOI != "" || m.PMID != "" || m.PMCID != "" || m.ArXiv != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.enrichFromOpenAlex(ctx, &mu, m)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.enrichFromEPMC(ctx, &mu, m)
		}()
	}

	if IsRxivDOI(m.DOI) {
		wg.Add(1)
		go func(doi, server, ver string) {
			defer wg.Done()
			r.enrichFromBioRxiv(ctx, &mu, m, doi, server, ver)
		}(m.DOI, ref.RxivServer, ref.RxivVersion)
	}

	if m.DOI != "" && !strings.HasPrefix(strings.ToLower(m.DOI), "10.48550/") {
		wg.Add(1)
		go func(doi string) {
			defer wg.Done()
			r.enrichFromUnpaywall(ctx, &mu, m, doi)
		}(m.DOI)
	}

	if ref.ZenodoID != "" {
		wg.Add(1)
		go func(zid string) {
			defer wg.Done()
			r.enrichFromZenodo(ctx, &mu, m, zid)
		}(ref.ZenodoID)
	}

	wg.Wait()

	if len(m.PDFCandidates) == 0 && m.ArXiv == "" && m.PMCID == "" && m.Title != "" {
		r.enrichPreprintByTitle(ctx, &mu, m)
	}

	if m.PMCID != "" {
		r.enrichFromPMCS3(ctx, &mu, m, m.PMCID)
	}
	if m.ArXiv != "" {
		m.addCandidate(fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", m.ArXiv), "arxiv-pdf", 10, nil)
	}
	if IsRxivDOI(m.DOI) {
		srv := ref.RxivServer
		if srv == "" {
			srv = "biorxiv"
		}
		ver := ref.RxivVersion
		if ver == "" {
			ver = "v1"
		}
		m.addCandidate(
			fmt.Sprintf("https://www.%s.org/content/%s%s.full.pdf", srv, m.DOI, ver),
			srv+"-direct-pdf",
			15,
			map[string]string{"Accept-Encoding": "identity"},
		)
	}
	if m.OpenReviewForum != "" {
		hdrs := openReviewAuthHeaders()
		if len(hdrs) > 0 {
			m.addCandidate(fmt.Sprintf("%s/pdf?id=%s", r.OpenReviewV2, m.OpenReviewForum), "openreview-v2-auth", 12, hdrs)
			m.addCandidate(fmt.Sprintf("%s/pdf?id=%s", r.OpenReviewV1, m.OpenReviewForum), "openreview-v1-auth", 13, hdrs)
		} else {
			m.addCandidate(fmt.Sprintf("%s/pdf?id=%s", r.OpenReviewV2, m.OpenReviewForum), "openreview-v2", 45, nil)
		}
	}

	m.extractReposFromText(m.Abstract)
	m.dedupCandidates()
	return m, nil
}

func (m *Meta) addCandidate(rawURL, source string, priority int, headers map[string]string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || (!strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://")) {
		return
	}
	if parsed := Parse(rawURL); parsed.ArXiv != "" {
		if m.ArXiv == "" {
			m.ArXiv = parsed.ArXiv
		}
		rawURL = fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", parsed.ArXiv)
		source = "arxiv-pdf"
		if priority > 15 {
			priority = 10
		}
	}
	m.PDFCandidates = append(m.PDFCandidates, PDFCandidate{
		URL:      rawURL,
		Source:   source,
		Priority: priority,
		Headers:  headers,
	})
}

func (m *Meta) dedupCandidates() {
	sort.SliceStable(m.PDFCandidates, func(i, j int) bool {
		return m.PDFCandidates[i].Priority < m.PDFCandidates[j].Priority
	})
	seen := make(map[string]bool, len(m.PDFCandidates))
	out := make([]PDFCandidate, 0, len(m.PDFCandidates))
	for _, c := range m.PDFCandidates {
		if seen[c.URL] {
			continue
		}
		seen[c.URL] = true
		out = append(out, c)
	}
	m.PDFCandidates = out
}

var reCodeRepo = regexp.MustCompile(`https?://(?:www\.)?(?:github\.com|huggingface\.co|gitlab\.com)/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)`)

func (m *Meta) addRepo(raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;)]}>\"'")
	raw = strings.TrimSuffix(raw, ".git")
	low := strings.ToLower(raw)
	if raw == "" || strings.Contains(low, "google/safevalues") || strings.Contains(low, "mathjax/") {
		return
	}
	for _, existing := range m.CodeRepos {
		if strings.EqualFold(existing, raw) {
			return
		}
	}
	m.CodeRepos = append(m.CodeRepos, raw)
}

func (m *Meta) extractReposFromText(text string) {
	for _, match := range reCodeRepo.FindAllStringSubmatch(text, -1) {
		full := match[0]
		parts := strings.Split(match[1], "/")
		if len(parts) >= 2 && parts[0] != "papers" && parts[0] != "datasets" && parts[0] != "spaces" {
			m.addRepo(full)
		}
	}
}

func (r *Resolver) resolveQuery(ctx context.Context, m *Meta, query string) {
	u := fmt.Sprintf("%s/works?search=%s&per-page=3", r.OpenAlexBase, url.QueryEscape(query))
	var res struct {
		Results []oaWork `json:"results"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err == nil && len(res.Results) > 0 {
		var mu sync.Mutex
		r.applyOpenAlexWork(&mu, m, &res.Results[0])
	}
}

func (r *Resolver) resolveByTitle(ctx context.Context, m *Meta, title string) {
	u := fmt.Sprintf("%s/works?filter=title.search:%s&per-page=5", r.OpenAlexBase, url.QueryEscape(title))
	var res struct {
		Results []oaWork `json:"results"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err == nil {
		normTarget := normTitle(title)
		var mu sync.Mutex
		for i := range res.Results {
			w := &res.Results[i]
			if normTitle(w.Title) == normTarget || strings.Contains(normTitle(w.Title), normTarget) {
				r.applyOpenAlexWork(&mu, m, w)
				break
			}
		}
	}
}

func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

type oaWork struct {
	ID              string   `json:"id"`
	DOI             string   `json:"doi"`
	Title           string   `json:"title"`
	PublicationYear int      `json:"publication_year"`
	Type            string   `json:"type"`
	IsRetracted     bool     `json:"is_retracted"`
	CitedByCount    int      `json:"cited_by_count"`
	ReferencedWorks []string `json:"referenced_works"`
	IDs             struct {
		DOI   string `json:"doi"`
		PMID  string `json:"pmid"`
		PMCID string `json:"pmcid"`
	} `json:"ids"`
	OpenAccess *struct {
		IsOA  bool   `json:"is_oa"`
		OAURL string `json:"oa_url"`
	} `json:"open_access"`
	PrimaryLocation *oaLocation  `json:"primary_location"`
	BestOALocation  *oaLocation  `json:"best_oa_location"`
	Locations       []oaLocation `json:"locations"`
	Authorships     []struct {
		Author struct {
			DisplayName string `json:"display_name"`
		} `json:"author"`
	} `json:"authorships"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
}

type oaLocation struct {
	IsOA           bool   `json:"is_oa"`
	LandingPageURL string `json:"landing_page_url"`
	PDFURL         string `json:"pdf_url"`
	License        string `json:"license"`
	Version        string `json:"version"`
	Source         *struct {
		DisplayName string `json:"display_name"`
		Type        string `json:"type"`
	} `json:"source"`
}

func (w *oaWork) rebuildAbstract() string {
	if len(w.AbstractInvertedIndex) == 0 {
		return ""
	}
	n := 0
	for _, posList := range w.AbstractInvertedIndex {
		for _, p := range posList {
			if p+1 > n {
				n = p + 1
			}
		}
	}
	if n == 0 || n > 25000 {
		return ""
	}
	words := make([]string, n)
	for word, posList := range w.AbstractInvertedIndex {
		for _, p := range posList {
			if p >= 0 && p < n {
				words[p] = word
			}
		}
	}
	return strings.Join(strings.Fields(strings.Join(words, " ")), " ")
}

func (r *Resolver) enrichFromOpenAlex(ctx context.Context, mu *sync.Mutex, m *Meta) {
	var idPath string
	switch {
	case m.DOI != "":
		idPath = "https://doi.org/" + m.DOI
	case m.ArXiv != "":
		idPath = "https://doi.org/10.48550/arXiv." + ArXivBase(m.ArXiv)
	case m.PMID != "":
		idPath = "pmid:" + m.PMID
	case m.PMCID != "":
		idPath = "pmcid:" + m.PMCID
	default:
		return
	}
	u := fmt.Sprintf("%s/works/%s", r.OpenAlexBase, url.PathEscape(idPath))
	var w oaWork
	if err := r.getJSON(ctx, u, nil, &w); err != nil || w.ID == "" {
		return
	}
	r.applyOpenAlexWork(mu, m, &w)
}

func (r *Resolver) applyOpenAlexWork(mu *sync.Mutex, m *Meta, w *oaWork) {
	mu.Lock()
	defer mu.Unlock()

	if m.OpenAlexID == "" && w.ID != "" {
		m.OpenAlexID = strings.TrimPrefix(w.ID, "https://openalex.org/")
	}
	if len(m.ReferencedWorks) == 0 && len(w.ReferencedWorks) > 0 {
		m.ReferencedWorks = w.ReferencedWorks
	}
	if m.Title == "" && w.Title != "" {
		m.Title = strings.Join(strings.Fields(w.Title), " ")
	}
	if m.Year == "" && w.PublicationYear > 0 {
		m.Year = fmt.Sprintf("%d", w.PublicationYear)
	}
	if m.Type == "" && w.Type != "" {
		m.Type = w.Type
	}
	if w.IsRetracted {
		m.Retracted = true
	}
	if w.CitedByCount > m.CitedBy {
		m.CitedBy = w.CitedByCount
	}
	if len(m.Authors) == 0 && len(w.Authorships) > 0 {
		for _, a := range w.Authorships {
			if a.Author.DisplayName != "" {
				m.Authors = append(m.Authors, a.Author.DisplayName)
			}
		}
	}
	if m.Venue == "" && w.PrimaryLocation != nil && w.PrimaryLocation.Source != nil {
		m.Venue = w.PrimaryLocation.Source.DisplayName
	}
	if m.Abstract == "" {
		if abs := w.rebuildAbstract(); abs != "" {
			m.Abstract = abs
		}
	}
	if doi := strings.TrimPrefix(w.DOI, "https://doi.org/"); doi != "" && m.DOI == "" {
		m.DOI = doi
		if am := reArXivDOI.FindStringSubmatch(doi); am != nil && m.ArXiv == "" {
			m.ArXiv = am[1]
		}
	}
	if pmid := strings.TrimPrefix(w.IDs.PMID, "https://pubmed.ncbi.nlm.nih.gov/"); pmid != "" && m.PMID == "" {
		m.PMID = strings.Trim(pmid, "/")
	}
	if pmcid := strings.TrimPrefix(w.IDs.PMCID, "https://www.ncbi.nlm.nih.gov/pmc/articles/"); pmcid != "" && m.PMCID == "" {
		if pm := rePMCID.FindStringSubmatch(strings.Trim(pmcid, "/")); pm != nil {
			m.PMCID = "PMC" + pm[1]
		}
	}
	if w.OpenAccess != nil {
		if w.OpenAccess.IsOA {
			m.IsOA = true
		}
		if w.OpenAccess.OAURL != "" {
			m.addCandidate(w.OpenAccess.OAURL, "openalex-oa-url", 22, nil)
		}
	}
	if w.BestOALocation != nil {
		if w.BestOALocation.IsOA {
			m.IsOA = true
		}
		if m.License == "" && w.BestOALocation.License != "" {
			m.License = w.BestOALocation.License
		}
		if w.BestOALocation.PDFURL != "" {
			m.addCandidate(w.BestOALocation.PDFURL, "openalex-best-oa", 20, nil)
		}
		if w.BestOALocation.LandingPageURL != "" {
			if parsed := Parse(w.BestOALocation.LandingPageURL); parsed.ArXiv != "" && m.ArXiv == "" {
				m.ArXiv = parsed.ArXiv
			}
		}
	}
	for _, loc := range w.Locations {
		if loc.PDFURL != "" {
			m.addCandidate(loc.PDFURL, "openalex-oa", 25, nil)
		}
		if loc.LandingPageURL != "" {
			if parsed := Parse(loc.LandingPageURL); parsed.ArXiv != "" && m.ArXiv == "" {
				m.ArXiv = parsed.ArXiv
			} else if parsed.DirectPDF != "" {
				m.addCandidate(parsed.DirectPDF, "openalex-derived-pdf", 24, nil)
			}
		}
	}
}

func (r *Resolver) enrichFromArXiv(ctx context.Context, mu *sync.Mutex, m *Meta, arxivID string) {
	u := fmt.Sprintf("%s?id_list=%s", r.ArXivAPIBase, url.QueryEscape(ArXivBase(arxivID)))
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return
	}
	var feed struct {
		Entries []struct {
			Title      string `xml:"title"`
			Summary    string `xml:"summary"`
			Published  string `xml:"published"`
			DOI        string `xml:"http://arxiv.org/schemas/atom doi"`
			JournalRef string `xml:"http://arxiv.org/schemas/atom journal_ref"`
			Comment    string `xml:"http://arxiv.org/schemas/atom comment"`
			Authors    []struct {
				Name string `xml:"name"`
			} `xml:"author"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil || len(feed.Entries) == 0 {
		return
	}
	e := feed.Entries[0]
	mu.Lock()
	defer mu.Unlock()

	if title := strings.Join(strings.Fields(e.Title), " "); title != "" {
		m.Title = title
	}
	if len(e.Published) >= 4 {
		m.Year = e.Published[:4]
	}
	if len(e.Authors) > 0 {
		m.Authors = nil
		for _, a := range e.Authors {
			if name := strings.TrimSpace(a.Name); name != "" {
				m.Authors = append(m.Authors, name)
			}
		}
	}
	if abs := strings.Join(strings.Fields(e.Summary), " "); abs != "" {
		m.Abstract = abs
	}
	if e.DOI != "" && m.DOI == "" {
		m.DOI = strings.TrimSpace(e.DOI)
	}
	if e.JournalRef != "" && m.Venue == "" {
		m.Venue = strings.Join(strings.Fields(e.JournalRef), " ")
	} else if m.Venue == "" {
		m.Venue = "arXiv"
	}
	if m.Type == "" {
		m.Type = "preprint"
	}
	m.extractReposFromText(e.Comment)
	m.extractReposFromText(e.Summary)
	m.addCandidate(fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", arxivID), "arxiv-pdf", 10, nil)
}

func (r *Resolver) enrichFromHFPapers(ctx context.Context, mu *sync.Mutex, m *Meta, arxivBaseID string) {
	u := fmt.Sprintf("%s/%s", r.HFPapersBase, url.PathEscape(arxivBaseID))
	var res struct {
		GitHubRepo string `json:"githubRepo"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err == nil && res.GitHubRepo != "" {
		mu.Lock()
		m.addRepo(res.GitHubRepo)
		mu.Unlock()
	}
}

func (r *Resolver) enrichFromBioRxiv(ctx context.Context, mu *sync.Mutex, m *Meta, doi, server, version string) {
	servers := []string{"biorxiv", "medrxiv"}
	if server == "medrxiv" {
		servers = []string{"medrxiv", "biorxiv"}
	}
	for _, srv := range servers {
		u := fmt.Sprintf("%s/%s/%s/na/json", r.BioRxivAPIBase, srv, doi)
		var res struct {
			Collection []struct {
				Title     string `json:"title"`
				Authors   string `json:"authors"`
				Date      string `json:"date"`
				Version   string `json:"version"`
				Type      string `json:"type"`
				License   string `json:"license"`
				Abstract  string `json:"abstract"`
				Published string `json:"published"`
			} `json:"collection"`
		}
		if err := r.getJSON(ctx, u, nil, &res); err != nil || len(res.Collection) == 0 {
			continue
		}
		last := res.Collection[len(res.Collection)-1]
		mu.Lock()
		if m.Title == "" && last.Title != "" {
			m.Title = strings.Join(strings.Fields(last.Title), " ")
		}
		if len(m.Authors) == 0 && last.Authors != "" {
			for _, a := range strings.Split(last.Authors, ";") {
				if a = strings.TrimSpace(a); a != "" {
					m.Authors = append(m.Authors, a)
				}
			}
		}
		if m.Year == "" && len(last.Date) >= 4 {
			m.Year = last.Date[:4]
		}
		if m.Venue == "" {
			m.Venue = srv
		}
		if m.Abstract == "" && last.Abstract != "" {
			m.Abstract = strings.Join(strings.Fields(last.Abstract), " ")
		}
		if m.License == "" && last.License != "" {
			m.License = last.License
		}
		if last.Published != "" && last.Published != "NA" {
			m.PublishedDOI = strings.TrimPrefix(last.Published, "https://doi.org/")
		}
		ver := version
		if ver == "" && last.Version != "" {
			ver = "v" + strings.TrimPrefix(last.Version, "v")
		}
		if ver == "" {
			ver = "v1"
		}
		pdfURL := fmt.Sprintf("https://www.%s.org/content/%s%s.full.pdf", srv, doi, ver)
		m.addCandidate(pdfURL, srv+"-pdf", 12, map[string]string{"Accept-Encoding": "identity"})
		mu.Unlock()
		return
	}
}

func (r *Resolver) enrichFromEPMC(ctx context.Context, mu *sync.Mutex, m *Meta) {
	var q string
	switch {
	case m.PMCID != "":
		q = "PMCID:" + m.PMCID
	case m.PMID != "":
		q = "EXT_ID:" + m.PMID + " AND SRC:MED"
	case m.DOI != "":
		q = fmt.Sprintf("DOI:\"%s\"", m.DOI)
	default:
		return
	}
	u := fmt.Sprintf("%s/search?format=json&resultType=core&pageSize=1&query=%s", r.EPMCBase, url.QueryEscape(q))
	var res struct {
		ResultList struct {
			Result []struct {
				ID          string `json:"id"`
				Source      string `json:"source"`
				PMID        string `json:"pmid"`
				PMCID       string `json:"pmcid"`
				DOI         string `json:"doi"`
				Title       string `json:"title"`
				AuthorStr   string `json:"authorString"`
				Journal     string `json:"journalTitle"`
				PubYear     string `json:"pubYear"`
				Abstract    string `json:"abstractText"`
				IsOA        string `json:"isOpenAccess"`
				InEPMC      string `json:"inEPMC"`
				InPMC       string `json:"inPMC"`
				CitedBy     int    `json:"citedByCount"`
				CommentList *struct {
					CommentCorrection []struct {
						Type      string `json:"type"`
						Reference string `json:"reference"`
					} `json:"commentCorrection"`
				} `json:"commentCorrectionList"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err != nil || len(res.ResultList.Result) == 0 {
		return
	}
	hit := res.ResultList.Result[0]
	mu.Lock()
	defer mu.Unlock()

	if m.Title == "" && hit.Title != "" {
		m.Title = strings.TrimSuffix(strings.Join(strings.Fields(hit.Title), " "), ".")
	}
	if m.Year == "" && hit.PubYear != "" {
		m.Year = hit.PubYear
	}
	if m.Venue == "" && hit.Journal != "" {
		m.Venue = hit.Journal
	}
	if len(m.Authors) == 0 && hit.AuthorStr != "" {
		for _, a := range strings.Split(strings.TrimSuffix(hit.AuthorStr, "."), ",") {
			if a = strings.TrimSpace(a); a != "" {
				m.Authors = append(m.Authors, a)
			}
		}
	}
	if m.Abstract == "" && hit.Abstract != "" {
		m.Abstract = strings.Join(strings.Fields(hit.Abstract), " ")
	}
	if m.PMID == "" && hit.PMID != "" {
		m.PMID = hit.PMID
	}
	if m.PMCID == "" && hit.PMCID != "" {
		m.PMCID = hit.PMCID
	}
	if m.DOI == "" && hit.DOI != "" {
		m.DOI = hit.DOI
	}
	if hit.CitedBy > m.CitedBy {
		m.CitedBy = hit.CitedBy
	}
	if hit.CommentList != nil {
		for _, cc := range hit.CommentList.CommentCorrection {
			low := strings.ToLower(cc.Type)
			if strings.Contains(low, "retract") {
				m.Retracted = true
				m.RetractionNotices = append(m.RetractionNotices, fmt.Sprintf("%s: %s", cc.Type, cc.Reference))
			} else if strings.Contains(low, "concern") {
				m.RetractionNotices = append(m.RetractionNotices, fmt.Sprintf("%s: %s", cc.Type, cc.Reference))
			}
		}
	}
}

func (r *Resolver) enrichFromPMCS3(ctx context.Context, mu *sync.Mutex, m *Meta, pmcid string) {
	u := fmt.Sprintf("%s/?list-type=2&prefix=%s.", r.PMCS3Base, url.QueryEscape(strings.ToUpper(pmcid)))
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return
	}
	var bucket struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &bucket); err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, obj := range bucket.Contents {
		if strings.HasSuffix(strings.ToLower(obj.Key), ".pdf") {
			m.addCandidate(fmt.Sprintf("%s/%s", r.PMCS3Base, obj.Key), "pmc-oa-s3", 11, nil)
		}
	}
}

func (r *Resolver) enrichFromUnpaywall(ctx context.Context, mu *sync.Mutex, m *Meta, doi string) {
	u := fmt.Sprintf("%s/%s?email=%s", r.UnpaywallBase, url.PathEscape(doi), url.QueryEscape(r.UnpaywallEmail))
	var res struct {
		Title          string `json:"title"`
		Year           int    `json:"year"`
		JournalName    string `json:"journal_name"`
		BestOALocation *struct {
			URLForPDF string `json:"url_for_pdf"`
			License   string `json:"license"`
		} `json:"best_oa_location"`
		OALocations []struct {
			URLForPDF string `json:"url_for_pdf"`
			HostType  string `json:"host_type"`
		} `json:"oa_locations"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()

	if m.Title == "" && res.Title != "" {
		m.Title = res.Title
	}
	if m.Year == "" && res.Year > 0 {
		m.Year = fmt.Sprintf("%d", res.Year)
	}
	if m.Venue == "" && res.JournalName != "" {
		m.Venue = res.JournalName
	}
	if res.BestOALocation != nil {
		if m.License == "" && res.BestOALocation.License != "" {
			m.License = res.BestOALocation.License
		}
		if res.BestOALocation.URLForPDF != "" {
			m.addCandidate(res.BestOALocation.URLForPDF, "unpaywall-best-oa", 22, nil)
		}
	}
	for _, loc := range res.OALocations {
		if loc.URLForPDF != "" {
			prio := 28
			if loc.HostType == "repository" {
				prio = 23
			}
			m.addCandidate(loc.URLForPDF, "unpaywall-oa", prio, nil)
		}
	}
}

func (r *Resolver) enrichFromZenodo(ctx context.Context, mu *sync.Mutex, m *Meta, zenodoID string) {
	u := fmt.Sprintf("%s/%s", r.ZenodoAPIBase, url.PathEscape(zenodoID))
	var res struct {
		Metadata struct {
			Title           string `json:"title"`
			PublicationDate string `json:"publication_date"`
			DOI             string `json:"doi"`
			Creators        []struct {
				Name string `json:"name"`
			} `json:"creators"`
		} `json:"metadata"`
		Files []struct {
			Key   string `json:"key"`
			Links struct {
				Self string `json:"self"`
			} `json:"links"`
		} `json:"files"`
	}
	if err := r.getJSON(ctx, u, nil, &res); err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if m.Title == "" && res.Metadata.Title != "" {
		m.Title = res.Metadata.Title
	}
	if m.Year == "" && len(res.Metadata.PublicationDate) >= 4 {
		m.Year = res.Metadata.PublicationDate[:4]
	}
	for _, f := range res.Files {
		if strings.HasSuffix(strings.ToLower(f.Key), ".pdf") && f.Links.Self != "" {
			m.addCandidate(f.Links.Self, "zenodo-pdf", 14, nil)
		}
	}
}

func openReviewAuthHeaders() map[string]string {
	if tok := strings.TrimSpace(os.Getenv("OPENREVIEW_TOKEN")); tok != "" {
		return map[string]string{"Authorization": "Bearer " + tok}
	}
	if cookie := strings.TrimSpace(os.Getenv("OPENREVIEW_COOKIE")); cookie != "" {
		return map[string]string{"Cookie": cookie}
	}
	return nil
}

const orGroupTerms = "the a an of and to in is we this that for with on are be it as by not"

var reDDGResultTitle = regexp.MustCompile(`(?s)class="result__a"[^>]*>(.*?)</a>`)
var reHTMLTags = regexp.MustCompile(`<[^>]+>`)

func (r *Resolver) enrichFromOpenReview(ctx context.Context, m *Meta) {
	forum := m.OpenReviewForum
	if forum == "" {
		return
	}
	hdrs := openReviewAuthHeaders()

	// 1. If authenticated, /notes?forum=<id> works directly.
	if len(hdrs) > 0 {
		for _, base := range []string{r.OpenReviewV2, r.OpenReviewV1} {
			u := fmt.Sprintf("%s/notes?forum=%s&limit=50", base, url.QueryEscape(forum))
			var res struct {
				Notes []openReviewNote `json:"notes"`
			}
			if err := r.getJSON(ctx, u, hdrs, &res); err == nil {
				for _, n := range res.Notes {
					if n.ID == forum || (n.Forum == forum && n.Replyto == "") {
						applyOpenReviewSubmission(m, n)
						return
					}
				}
			}
		}
	}

	// 2. If group is known (forum@group), group search works anonymously.
	if m.OpenReviewGroup != "" {
		grp := strings.TrimSuffix(m.OpenReviewGroup, "/") + "/"
		for _, base := range r.openReviewBasesForGroup(m.OpenReviewGroup) {
			q := url.Values{
				"term":    {orGroupTerms},
				"type":    {"terms"},
				"content": {"all"},
				"source":  {"all"},
				"group":   {grp},
				"limit":   {"200"},
			}
			var res struct {
				Notes []openReviewNote `json:"notes"`
			}
			if err := r.getJSON(ctx, base+"/notes/search?"+q.Encode(), nil, &res); err == nil && len(res.Notes) > 0 {
				var forumNotes []openReviewNote
				for _, n := range res.Notes {
					if n.Forum == forum {
						forumNotes = append(forumNotes, n)
					}
				}
				if len(forumNotes) > 0 {
					m.orNotes = forumNotes
					for _, n := range forumNotes {
						if n.ID == forum || n.Replyto == "" {
							applyOpenReviewSubmission(m, n)
							return
						}
					}
					// v1 group searches often return only replies, each carrying forumContent.
					for _, n := range forumNotes {
						if len(n.ForumContent) > 0 {
							applyOpenReviewContent(m, n.ForumContent, n.Cdate)
							return
						}
					}
				}
			}
		}
	}

	// 3. Bare forum ID without token or group: resolve title via DuckDuckGo HTML, then search /notes/search by title.
	candTitle := r.resolveOpenReviewTitleViaDDG(ctx, forum)
	if candTitle != "" && m.Title == "" {
		m.Title = candTitle
	}
	searchTerm := candTitle
	if searchTerm == "" {
		searchTerm = forum
	}
	for _, base := range []string{r.OpenReviewV2, r.OpenReviewV1} {
		q := url.Values{
			"term":    {searchTerm},
			"type":    {"terms"},
			"content": {"all"},
			"source":  {"forum"},
			"limit":   {"25"},
		}
		var res struct {
			Notes []openReviewNote `json:"notes"`
		}
		if err := r.getJSON(ctx, base+"/notes/search?"+q.Encode(), nil, &res); err != nil {
			continue
		}
		for _, n := range res.Notes {
			if n.ID == forum || (n.Forum == forum && n.Replyto == "") {
				applyOpenReviewSubmission(m, n)
				return
			}
		}
	}
}

func (r *Resolver) resolveOpenReviewTitleViaDDG(ctx context.Context, forum string) string {
	u := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(forum+" openreview"))
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return ""
	}
	for _, match := range reDDGResultTitle.FindAllStringSubmatch(string(body), 8) {
		txt := strings.TrimSpace(reHTMLTags.ReplaceAllString(match[1], ""))
		for _, suf := range []string{" - OpenReview", " | OpenReview"} {
			if strings.HasSuffix(txt, suf) {
				title := strings.TrimSpace(strings.TrimSuffix(txt, suf))
				if title != "" && !strings.EqualFold(title, "Venues") && !strings.EqualFold(title, "Search") && !strings.EqualFold(title, "Login") {
					return title
				}
			}
		}
	}
	return ""
}

var reORV1Group = regexp.MustCompile(`/Paper\d+/?$`)
var reORV2Group = regexp.MustCompile(`/Submission\d+/?$`)

func (r *Resolver) openReviewBasesForGroup(group string) []string {
	if reORV1Group.MatchString(group) {
		return []string{r.OpenReviewV1}
	}
	if reORV2Group.MatchString(group) {
		return []string{r.OpenReviewV2}
	}
	return []string{r.OpenReviewV2, r.OpenReviewV1}
}

func applyOpenReviewContent(m *Meta, content map[string]any, cdate int64) {
	if title := orStr(content, "title"); title != "" {
		m.Title = title
	}
	if abs := orStr(content, "abstract"); abs != "" && m.Abstract == "" {
		m.Abstract = abs
	}
	if venue := orStr(content, "venue"); venue != "" && m.Venue == "" {
		m.Venue = venue
	}
	if code := orStr(content, "code"); code != "" {
		m.addRepo(code)
	}
	if len(m.Authors) == 0 {
		m.Authors = orSlice(content, "authors")
	}
	if m.Year == "" && cdate > 0 {
		m.Year = time.UnixMilli(cdate).UTC().Format("2006")
	}
}

func applyOpenReviewSubmission(m *Meta, n openReviewNote) {
	applyOpenReviewContent(m, n.Content, n.Cdate)
	if m.Title == "" && len(n.ForumContent) > 0 {
		applyOpenReviewContent(m, n.ForumContent, n.Cdate)
	}
	if m.OpenReviewGroup == "" {
		if len(n.Signatures) > 0 {
			sig := n.Signatures[0]
			if idx := strings.LastIndex(sig, "/"); idx > 0 && !strings.HasPrefix(sig, "~") {
				m.OpenReviewGroup = sig[:idx]
			}
		}
		if m.OpenReviewGroup == "" && n.Domain != "" && n.Number > 0 {
			m.OpenReviewGroup = fmt.Sprintf("%s/Submission%d", n.Domain, n.Number)
		}
		if m.OpenReviewGroup == "" && n.Invitation != "" && n.Number > 0 {
			if venue, _, ok := strings.Cut(n.Invitation, "/-/"); ok {
				m.OpenReviewGroup = fmt.Sprintf("%s/Paper%d", venue, n.Number)
			}
		}
	}
}

type openReviewNote struct {
	ID           string         `json:"id"`
	Forum        string         `json:"forum"`
	Replyto      string         `json:"replyto"`
	Number       int            `json:"number"`
	Domain       string         `json:"domain"`
	Cdate        int64          `json:"cdate"`
	Signatures   []string       `json:"signatures"`
	Invitations  []string       `json:"invitations"`
	Invitation   string         `json:"invitation"`
	Content      map[string]any `json:"content"`
	ForumContent map[string]any `json:"forumContent"`
}

func orStr(content map[string]any, key string) string {
	v, ok := content[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case map[string]any:
		if inner, ok := val["value"].(string); ok {
			return strings.TrimSpace(inner)
		}
	}
	return ""
}

func orSlice(content map[string]any, key string) []string {
	v, ok := content[key]
	if !ok || v == nil {
		return nil
	}
	extract := func(arr []any) []string {
		var out []string
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	switch val := v.(type) {
	case []any:
		return extract(val)
	case map[string]any:
		if inner, ok := val["value"].([]any); ok {
			return extract(inner)
		}
	}
	return nil
}

// Slug generates a filesystem-safe <year>-<author>-<short-title> identifier.
func (m *Meta) Slug() string {
	var parts []string
	if m.Year != "" {
		parts = append(parts, m.Year)
	}
	if len(m.Authors) > 0 {
		fields := strings.Fields(m.Authors[0])
		if len(fields) > 0 {
			last := slugToken(fields[len(fields)-1])
			if last != "" {
				parts = append(parts, last)
			}
		}
	}
	if m.Title != "" {
		words := strings.FieldsFunc(strings.ToLower(m.Title), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		kept := 0
		for _, w := range words {
			if len(w) <= 2 && w != "ai" && w != "rl" && w != "3d" {
				continue
			}
			parts = append(parts, w)
			kept++
			if kept >= 5 {
				break
			}
		}
	}
	if len(parts) == 0 {
		switch {
		case m.ArXiv != "":
			return "arxiv-" + slugToken(m.ArXiv)
		case m.PMCID != "":
			return strings.ToLower(m.PMCID)
		case m.DOI != "":
			return "doi-" + slugToken(m.DOI)
		case m.OpenReviewForum != "":
			return "openreview-" + slugToken(m.OpenReviewForum)
		default:
			return "paper"
		}
	}
	return strings.Join(parts, "-")
}

func slugToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// BibTeX formats the resolved metadata as a standard BibTeX entry.
func (m *Meta) BibTeX() string {
	key := strings.ReplaceAll(m.Slug(), "-", "_")
	entryType := "article"
	if strings.Contains(strings.ToLower(m.Type), "proceedings") || strings.Contains(strings.ToLower(m.Venue), "conference") || m.OpenReviewForum != "" {
		entryType = "inproceedings"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "@%s{%s,\n", entryType, key)
	if m.Title != "" {
		fmt.Fprintf(&b, "  title = {%s},\n", m.Title)
	}
	if len(m.Authors) > 0 {
		fmt.Fprintf(&b, "  author = {%s},\n", strings.Join(m.Authors, " and "))
	}
	if m.Year != "" {
		fmt.Fprintf(&b, "  year = {%s},\n", m.Year)
	}
	if m.Venue != "" {
		if entryType == "inproceedings" {
			fmt.Fprintf(&b, "  booktitle = {%s},\n", m.Venue)
		} else {
			fmt.Fprintf(&b, "  journal = {%s},\n", m.Venue)
		}
	}
	if m.DOI != "" {
		fmt.Fprintf(&b, "  doi = {%s},\n", m.DOI)
	}
	if m.ArXiv != "" {
		fmt.Fprintf(&b, "  eprint = {%s},\n  archivePrefix = {arXiv},\n", ArXivBase(m.ArXiv))
	}
	if m.PMID != "" {
		fmt.Fprintf(&b, "  pmid = {%s},\n", m.PMID)
	}
	if m.OpenReviewForum != "" {
		fmt.Fprintf(&b, "  url = {https://openreview.net/forum?id=%s},\n", m.OpenReviewForum)
	}
	b.WriteString("}\n")
	return b.String()
}

func (r *Resolver) resolveByPII(ctx context.Context, m *Meta, pii string) error {
	u := "https://api.crossref.org/works?filter=alternative-id:" + url.QueryEscape(pii) + "&rows=1"
	body, err := r.getBytes(ctx, u, nil)
	if err != nil {
		return err
	}
	var resp struct {
		Message struct {
			Items []struct {
				DOI   string   `json:"DOI"`
				Title []string `json:"title"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if len(resp.Message.Items) > 0 && resp.Message.Items[0].DOI != "" {
		m.DOI = strings.ToLower(strings.TrimSpace(resp.Message.Items[0].DOI))
		if m.Title == "" && len(resp.Message.Items[0].Title) > 0 {
			m.Title = strings.TrimSpace(resp.Message.Items[0].Title[0])
		}
	}
	return nil
}

func (r *Resolver) enrichPreprintByTitle(ctx context.Context, mu *sync.Mutex, m *Meta) {
	wantKey := normalizeTitleKey(m.Title)
	if len(wantKey) < 16 {
		return
	}
	subCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := r.searchArXiv(subCtx, SearchOptions{Query: `ti:"` + m.Title + `"`}, 5)
		if err != nil {
			return
		}
		for _, it := range items {
			if normalizeTitleKey(it.Title) == wantKey && it.ArXiv != "" {
				mu.Lock()
				if m.ArXiv == "" {
					m.ArXiv = it.ArXiv
				}
				m.addCandidate("https://arxiv.org/pdf/"+ArXivBase(it.ArXiv)+".pdf", "arxiv-preprint-match", 42, nil)
				mu.Unlock()
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := r.searchOpenAlex(subCtx, SearchOptions{Query: m.Title, OAOnly: true}, 5)
		if err != nil {
			return
		}
		for _, it := range items {
			if normalizeTitleKey(it.Title) == wantKey {
				mu.Lock()
				if it.PDFURL != "" {
					m.addCandidate(it.PDFURL, "openalex-preprint-match", 44, nil)
				}
				if m.PMCID == "" && it.PMCID != "" {
					m.PMCID = it.PMCID
				}
				mu.Unlock()
				return
			}
		}
	}()
	wg.Wait()
}
