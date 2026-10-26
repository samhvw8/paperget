package paper

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Ref holds parsed identifiers from a user-supplied argument (ID, URL, or query).
type Ref struct {
	Raw             string `json:"raw"`
	DOI             string `json:"doi,omitempty"`
	ArXiv           string `json:"arxiv,omitempty"`
	PMID            string `json:"pmid,omitempty"`
	PMCID           string `json:"pmcid,omitempty"`
	RxivServer      string `json:"rxiv_server,omitempty"` // "biorxiv" or "medrxiv"
	RxivVersion     string `json:"rxiv_version,omitempty"`
	OpenReviewForum string `json:"openreview_forum,omitempty"`
	OpenReviewGroup string `json:"openreview_group,omitempty"`
	ACL             string `json:"acl,omitempty"`
	ZenodoID        string `json:"zenodo_id,omitempty"`
	HALID           string `json:"hal_id,omitempty"`
	AnnaMD5         string `json:"anna_md5,omitempty"`
	DirectPDF       string `json:"direct_pdf,omitempty"`
	Query           string `json:"query,omitempty"`
}

var (
	reDOI       = regexp.MustCompile(`(?i)^(10\.\d{4,9}/[^\s"<>]+)$`)
	reDOIFind   = regexp.MustCompile(`(?i)(10\.\d{4,9}/[^\s"<>?#]+)`)
	reArXivNew  = regexp.MustCompile(`^(\d{4}\.\d{4,5})(v\d+)?$`)
	reArXivOld  = regexp.MustCompile(`^([a-z][a-z.-]*(?:\.[A-Z]{2})?/\d{7})(v\d+)?$`)
	reArXivDOI  = regexp.MustCompile(`^(?i)10\.48550/arxiv\.(.+)$`)
	rePMCID     = regexp.MustCompile(`^(?i)pmc(\d+)$`)
	rePMIDPref  = regexp.MustCompile(`^(?i)pmid:\s*(\d+)$`)
	reDigits    = regexp.MustCompile(`^\d{5,10}$`)
	reMD5       = regexp.MustCompile(`^(?i)(?:md5:)?([a-f0-9]{32})$`)
	reRxivPath  = regexp.MustCompile(`^/content/(?:early/\d{4}/\d{2}/\d{2}/)?(10\.(?:1101|64898)/[\d.]+)(v\d+)?`)
	reZenodoDOI = regexp.MustCompile(`^(?i)10\.5281/zenodo\.(\d+)$`)
	reORRef     = regexp.MustCompile(`^([A-Za-z0-9_-]{6,20})@([A-Za-z0-9._/-]+)$`)
)

// Parse turns a raw CLI argument (identifier, URL, or title query) into a Ref.
func Parse(raw string) Ref {
	s := strings.TrimSpace(raw)
	ref := Ref{Raw: s}
	if s == "" {
		return ref
	}

	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "doi:"):
		return applyDOI(ref, strings.TrimSpace(s[4:]))
	case strings.HasPrefix(lower, "arxiv:"):
		id := strings.TrimSuffix(strings.TrimSpace(s[6:]), ".pdf")
		if IsArXivID(id) {
			ref.ArXiv = id
			return ref
		}
	case strings.HasPrefix(lower, "openreview:"):
		return parseOpenReviewRef(ref, strings.TrimSpace(s[11:]))
	}
	if m := rePMIDPref.FindStringSubmatch(s); m != nil {
		ref.PMID = m[1]
		return ref
	}
	if m := rePMCID.FindStringSubmatch(s); m != nil {
		ref.PMCID = "PMC" + m[1]
		return ref
	}
	if m := reMD5.FindStringSubmatch(s); m != nil {
		ref.AnnaMD5 = strings.ToLower(m[1])
		return ref
	}
	if IsArXivID(s) {
		ref.ArXiv = s
		return ref
	}
	if reDOI.MatchString(s) {
		return applyDOI(ref, s)
	}
	if m := reORRef.FindStringSubmatch(s); m != nil {
		ref.OpenReviewForum = m[1]
		ref.OpenReviewGroup = m[2]
		return ref
	}

	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if u, err := url.Parse(s); err == nil {
			if parsed, ok := parseURL(ref, u); ok {
				return parsed
			}
		}
	}

	if reDigits.MatchString(s) {
		ref.PMID = s
		return ref
	}

	ref.Query = s
	return ref
}

func parseOpenReviewRef(ref Ref, val string) Ref {
	if m := reORRef.FindStringSubmatch(val); m != nil {
		ref.OpenReviewForum = m[1]
		ref.OpenReviewGroup = m[2]
		return ref
	}
	ref.OpenReviewForum = val
	return ref
}

func applyDOI(ref Ref, doi string) Ref {
	doi = strings.TrimRight(strings.TrimSpace(doi), ".,;)")
	ref.DOI = doi
	if m := reArXivDOI.FindStringSubmatch(doi); m != nil && IsArXivID(m[1]) {
		ref.ArXiv = m[1]
	}
	if m := reZenodoDOI.FindStringSubmatch(doi); m != nil {
		ref.ZenodoID = m[1]
	}
	if IsRxivDOI(doi) {
		if ref.RxivServer == "" {
			ref.RxivServer = "biorxiv"
		}
	}
	return ref
}

func parseURL(ref Ref, u *url.URL) (Ref, bool) {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := u.EscapedPath()
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")

	switch {
	case host == "doi.org" || host == "dx.doi.org":
		if trimmed != "" {
			return applyDOI(ref, trimmed), true
		}

	case host == "arxiv.org" || host == "export.arxiv.org" || host == "alphaxiv.org":
		if len(parts) >= 2 && (parts[0] == "abs" || parts[0] == "pdf" || parts[0] == "html" || parts[0] == "e-print" || parts[0] == "src") {
			id := strings.TrimSuffix(strings.Join(parts[1:], "/"), ".pdf")
			if IsArXivID(id) {
				ref.ArXiv = id
				return ref, true
			}
		}

	case host == "huggingface.co" || host == "hf.co":
		if len(parts) >= 2 && parts[0] == "papers" {
			id := strings.TrimSuffix(parts[1], ".pdf")
			if IsArXivID(id) {
				ref.ArXiv = id
				return ref, true
			}
		}

	case host == "biorxiv.org" || host == "medrxiv.org":
		server := strings.TrimSuffix(host, ".org")
		if m := reRxivPath.FindStringSubmatch(path); m != nil {
			ref = applyDOI(ref, strings.TrimSuffix(m[1], "."))
			ref.RxivServer = server
			ref.RxivVersion = m[2]
			return ref, true
		}

	case host == "openreview.net" || host == "api2.openreview.net" || host == "api.openreview.net":
		if id := u.Query().Get("id"); id != "" {
			ref.OpenReviewForum = id
			return ref, true
		}

	case host == "pmc.ncbi.nlm.nih.gov":
		if len(parts) >= 2 && parts[0] == "articles" {
			if m := rePMCID.FindStringSubmatch(parts[1]); m != nil {
				ref.PMCID = "PMC" + m[1]
				return ref, true
			}
		}

	case host == "ncbi.nlm.nih.gov":
		if len(parts) >= 3 && parts[0] == "pmc" && parts[1] == "articles" {
			if m := rePMCID.FindStringSubmatch(parts[2]); m != nil {
				ref.PMCID = "PMC" + m[1]
				return ref, true
			}
		}
		if len(parts) >= 2 && parts[0] == "pubmed" && reDigits.MatchString(parts[1]) {
			ref.PMID = parts[1]
			return ref, true
		}

	case host == "pubmed.ncbi.nlm.nih.gov":
		if len(parts) >= 1 && reDigits.MatchString(parts[0]) {
			ref.PMID = parts[0]
			return ref, true
		}

	case host == "europepmc.org":
		if len(parts) >= 3 && (parts[0] == "article" || parts[0] == "abstract") {
			src := strings.ToUpper(parts[1])
			if src == "MED" && reDigits.MatchString(parts[2]) {
				ref.PMID = parts[2]
				return ref, true
			}
			if src == "PMC" {
				if m := rePMCID.FindStringSubmatch(parts[2]); m != nil {
					ref.PMCID = "PMC" + m[1]
					return ref, true
				}
			}
		}
		if len(parts) >= 2 && parts[0] == "articles" {
			if m := rePMCID.FindStringSubmatch(parts[1]); m != nil {
				ref.PMCID = "PMC" + m[1]
				return ref, true
			}
		}

	case host == "aclanthology.org":
		if len(parts) >= 1 && parts[0] != "" {
			id := strings.TrimSuffix(parts[0], ".pdf")
			id = strings.TrimSuffix(id, ".bib")
			ref.ACL = id
			ref.DirectPDF = fmt.Sprintf("https://aclanthology.org/%s.pdf", id)
			return ref, true
		}

	case host == "proceedings.mlr.press":
		if strings.HasSuffix(path, ".pdf") {
			ref.DirectPDF = u.String()
			return ref, true
		}
		if strings.HasSuffix(path, ".html") && len(parts) >= 2 {
			slug := strings.TrimSuffix(parts[len(parts)-1], ".html")
			ref.DirectPDF = fmt.Sprintf("https://proceedings.mlr.press/%s/%s/%s.pdf", parts[0], slug, slug)
			return ref, true
		}

	case host == "openaccess.thecvf.com":
		if strings.HasSuffix(path, ".pdf") {
			ref.DirectPDF = u.String()
			return ref, true
		}
		if strings.Contains(path, "/html/") && strings.HasSuffix(path, "_paper.html") {
			pdfPath := strings.Replace(path, "/html/", "/papers/", 1)
			pdfPath = strings.TrimSuffix(pdfPath, ".html") + ".pdf"
			ref.DirectPDF = "https://openaccess.thecvf.com" + pdfPath
			return ref, true
		}

	case host == "papers.nips.cc" || host == "proceedings.neurips.cc":
		if strings.HasSuffix(path, ".pdf") {
			ref.DirectPDF = u.String()
			return ref, true
		}
		if strings.Contains(path, "/hash/") && strings.HasSuffix(path, ".html") {
			pdfPath := strings.Replace(path, "/hash/", "/file/", 1)
			pdfPath = strings.Replace(pdfPath, "-Abstract-Conference.html", "-Paper-Conference.pdf", 1)
			pdfPath = strings.Replace(pdfPath, "-Abstract.html", "-Paper.pdf", 1)
			ref.DirectPDF = fmt.Sprintf("https://%s%s", host, pdfPath)
			return ref, true
		}

	case host == "hal.science" || strings.HasSuffix(host, ".hal.science") || host == "hal.archives-ouvertes.fr":
		if len(parts) >= 1 && strings.HasPrefix(parts[0], "hal-") {
			ref.HALID = parts[0]
			ref.DirectPDF = fmt.Sprintf("https://hal.science/%s/document", parts[0])
			return ref, true
		}

	case host == "zenodo.org":
		if len(parts) >= 2 && (parts[0] == "records" || parts[0] == "record") && reDigits.MatchString(parts[1]) {
			ref.ZenodoID = parts[1]
			ref.DOI = "10.5281/zenodo." + parts[1]
			return ref, true
		}

	case strings.HasPrefix(host, "annas-archive."):
		if len(parts) >= 2 && parts[0] == "scidb" {
			doi := strings.Join(parts[1:], "/")
			return applyDOI(ref, doi), true
		}
		if len(parts) >= 2 && parts[0] == "md5" && reMD5.MatchString(parts[1]) {
			ref.AnnaMD5 = strings.ToLower(parts[1])
			return ref, true
		}
		if q := u.Query().Get("q"); q != "" {
			return Parse(q), true
		}

	case strings.HasPrefix(host, "sci-hub."):
		if trimmed != "" {
			if m := reDOIFind.FindStringSubmatch(trimmed); m != nil {
				return applyDOI(ref, m[1]), true
			}
		}
	}

	if strings.HasSuffix(strings.ToLower(path), ".pdf") {
		ref.DirectPDF = u.String()
		return ref, true
	}
	if m := reDOIFind.FindStringSubmatch(path); m != nil {
		return applyDOI(ref, m[1]), true
	}

	return ref, false
}

// IsArXivID reports whether id is a valid new or old style arXiv identifier.
func IsArXivID(id string) bool {
	return reArXivNew.MatchString(id) || reArXivOld.MatchString(id)
}

// ArXivBase strips the vN version suffix from an arXiv ID.
func ArXivBase(id string) string {
	if m := reArXivNew.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if m := reArXivOld.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return id
}

// IsRxivDOI reports whether doi uses a bioRxiv/medRxiv DOI prefix (10.1101 or 10.64898).
func IsRxivDOI(doi string) bool {
	low := strings.ToLower(strings.TrimSpace(doi))
	return strings.HasPrefix(low, "10.1101/") || strings.HasPrefix(low, "10.64898/")
}
