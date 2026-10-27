package paper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	defaultSciHubMirrors = []string{
		"https://sci-hub.ru",
		"https://sci-hub.vn",
		"https://sci-hub.se",
		"https://sci-hub.st",
	}
	defaultAnnaMirrors = []string{
		"https://annas-archive.gd",
		"https://annas-archive.gl",
		"https://annas-archive.pk",
	}
	reAltchaChallenge = regexp.MustCompile(`challengeurl\s*=\s*["'](/captcha/challenge/(\d+))["']`)
	reSciHubPDF       = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']([^"']*\.pdf[^"']*)["']`)
	reAnnaMD5Link     = regexp.MustCompile(`/md5/([a-f0-9]{32})`)
	reLibgenGetLink   = regexp.MustCompile(`(?i)href=["']((?:https?://[^"']+/)?get\.php\?md5=[a-f0-9]{32}[^"']*)["']`)
)

type altchaChallenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxNumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

type altchaSolution struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int    `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
	Took      int64  `json:"took"`
}

func (r *Resolver) downloadFromShadow(ctx context.Context, m *Meta, opts DownloadOptions) ([]byte, string, error) {
	var errs []string

	dois := []string{}
	if m.DOI != "" && !strings.HasPrefix(strings.ToLower(m.DOI), "10.48550/") {
		dois = append(dois, m.DOI)
	}
	if m.PublishedDOI != "" && m.PublishedDOI != m.DOI && !strings.HasPrefix(strings.ToLower(m.PublishedDOI), "10.48550/") {
		dois = append(dois, m.PublishedDOI)
	}

	// 1. Try Sci-Hub / Sci-Net with automatic Altcha SHA-256 PoW solver
	for _, doi := range dois {
		data, src, err := r.downloadFromSciHub(ctx, doi, opts.SciHubBase)
		if err == nil && len(data) > 0 {
			return data, src, nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("scihub(%s): %v", doi, err))
		}
	}

	// 2. Try Anna's Archive (Fast Download API if ANNAS_ARCHIVE_KEY set, plus /scidb/<doi> and /md5/<md5>)
	if m.Ref.AnnaMD5 != "" || len(dois) > 0 {
		data, src, err := r.downloadFromAnnasArchive(ctx, m.Ref.AnnaMD5, dois, opts.AnnaBase)
		if err == nil && len(data) > 0 {
			return data, src, nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("annas-archive: %v", err))
		}
	}

	// 3. Try LibGen scimag direct lookup by DOI
	for _, doi := range dois {
		data, src, err := r.downloadFromLibgenScimag(ctx, doi)
		if err == nil && len(data) > 0 {
			return data, src, nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("libgen(%s): %v", doi, err))
		}
	}

	if len(errs) == 0 {
		return nil, "", errors.New("no DOI or MD5 available for shadow-library lookup")
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

func (r *Resolver) downloadFromSciHub(ctx context.Context, doi, overrideBase string) ([]byte, string, error) {
	mirrors := defaultSciHubMirrors
	if overrideBase != "" {
		mirrors = []string{strings.TrimRight(overrideBase, "/")}
	}

	var lastErr error
	for _, mirror := range mirrors {
		data, err := r.trySciHubMirror(ctx, mirror, doi)
		if err == nil && len(data) > 0 {
			return data, "scihub", nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func (r *Resolver) trySciHubMirror(ctx context.Context, mirror, doi string) ([]byte, error) {
	mCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 20 * time.Second}
	targetURL := fmt.Sprintf("%s/%s", strings.TrimRight(mirror, "/"), strings.TrimLeft(doi, "/"))

	cookies := loadCachedCookies("scihub", mirror)
	req1, err := http.NewRequestWithContext(mCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req1.Header.Set("User-Agent", DefaultUserAgent)
	if cookieHdr := formatCookieMap(cookies); cookieHdr != "" {
		req1.Header.Set("Cookie", cookieHdr)
	}

	resp1, err := client.Do(req1)
	if err != nil {
		return nil, err
	}
	body1, _ := io.ReadAll(io.LimitReader(resp1.Body, MaxPDFBytes))
	resp1.Body.Close()

	// Check if mirror directly returned a PDF
	if cleaned, ok := VerifyAndTrimPDF(body1); ok {
		return cleaned, nil
	}

	finalURL := resp1.Request.URL
	origin := fmt.Sprintf("%s://%s", finalURL.Scheme, finalURL.Host)
	if origin != mirror {
		// Load cookies for the redirected origin too
		for k, v := range loadCachedCookies("scihub", origin) {
			cookies[k] = v
		}
	}
	mergeSetCookies(cookies, resp1.Header.Values("Set-Cookie"))
	htmlStr := string(body1)

	// Check if Altcha PoW challenge is present
	if cm := reAltchaChallenge.FindStringSubmatch(htmlStr); cm != nil {
		challengePath := cm[1]
		challengeID := cm[2]

		chReq, err := http.NewRequestWithContext(mCtx, http.MethodGet, origin+challengePath, nil)
		if err != nil {
			return nil, err
		}
		chReq.Header.Set("User-Agent", DefaultUserAgent)
		chReq.Header.Set("Cookie", formatCookieMap(cookies))
		chReq.Header.Set("Referer", finalURL.String())

		chResp, err := client.Do(chReq)
		if err != nil {
			return nil, fmt.Errorf("altcha challenge fetch: %w", err)
		}
		mergeSetCookies(cookies, chResp.Header.Values("Set-Cookie"))
		chBody, _ := io.ReadAll(io.LimitReader(chResp.Body, 1<<20))
		chResp.Body.Close()

		var ch altchaChallenge
		if err := json.Unmarshal(chBody, &ch); err != nil {
			return nil, fmt.Errorf("altcha challenge decode: %w", err)
		}

		payloadB64, err := solveAltchaPoW(ch)
		if err != nil {
			return nil, err
		}

		solBody, _ := json.Marshal(map[string]string{"captcha": payloadB64})
		solURL := fmt.Sprintf("%s/captcha/solution/%s", origin, challengeID)
		solReq, err := http.NewRequestWithContext(mCtx, http.MethodPost, solURL, bytes.NewReader(solBody))
		if err != nil {
			return nil, err
		}
		solReq.Header.Set("User-Agent", DefaultUserAgent)
		solReq.Header.Set("Cookie", formatCookieMap(cookies))
		solReq.Header.Set("Content-Type", "application/json")
		solReq.Header.Set("Referer", finalURL.String())

		solResp, err := client.Do(solReq)
		if err != nil {
			return nil, fmt.Errorf("altcha solution post: %w", err)
		}
		mergeSetCookies(cookies, solResp.Header.Values("Set-Cookie"))
		_, _ = io.Copy(io.Discard, solResp.Body)
		solResp.Body.Close()

		saveCachedCookies("scihub", origin, cookies)

		// Re-fetch paper page on the same origin with solved session cookie
		retryReq, err := http.NewRequestWithContext(mCtx, http.MethodGet, finalURL.String(), nil)
		if err != nil {
			return nil, err
		}
		retryReq.Header.Set("User-Agent", DefaultUserAgent)
		retryReq.Header.Set("Cookie", formatCookieMap(cookies))
		retryReq.Header.Set("Referer", finalURL.String())

		retryResp, err := client.Do(retryReq)
		if err != nil {
			return nil, err
		}
		mergeSetCookies(cookies, retryResp.Header.Values("Set-Cookie"))
		retryBody, _ := io.ReadAll(io.LimitReader(retryResp.Body, MaxPDFBytes))
		retryResp.Body.Close()

		if cleaned, ok := VerifyAndTrimPDF(retryBody); ok {
			return cleaned, nil
		}
		htmlStr = string(retryBody)
	}

	// Extract PDF URL from Sci-Hub HTML
	matches := reSciHubPDF.FindAllStringSubmatch(htmlStr, -1)
	if len(matches) == 0 {
		return nil, errors.New("paper not in Sci-Hub or no PDF link on page")
	}

	for _, match := range matches {
		rawPDF := strings.Split(match[1], "#")[0]
		pdfURL, err := finalURL.Parse(rawPDF)
		if err != nil {
			continue
		}
		pdfReq, err := http.NewRequestWithContext(mCtx, http.MethodGet, pdfURL.String(), nil)
		if err != nil {
			continue
		}
		pdfReq.Header.Set("User-Agent", DefaultUserAgent)
		pdfReq.Header.Set("Cookie", formatCookieMap(cookies))
		pdfReq.Header.Set("Referer", finalURL.String())

		pdfResp, err := client.Do(pdfReq)
		if err != nil {
			continue
		}
		pdfBytes, _ := io.ReadAll(io.LimitReader(pdfResp.Body, MaxPDFBytes))
		pdfResp.Body.Close()

		if cleaned, ok := VerifyAndTrimPDF(pdfBytes); ok {
			saveCachedCookies("scihub", origin, cookies)
			return cleaned, nil
		}
	}

	return nil, errors.New("Sci-Hub PDF link did not return valid PDF bytes")
}

// SolveAltchaPoW solves an Altcha SHA-256 Proof-of-Work challenge and returns the base64 payload.
func solveAltchaPoW(ch altchaChallenge) (string, error) {
	t0 := time.Now()
	maxN := ch.MaxNumber
	if maxN <= 0 || maxN > 5_000_000 {
		maxN = 1_000_000
	}
	target, err := hex.DecodeString(strings.ToLower(ch.Challenge))
	if err != nil || len(target) != sha256.Size {
		return "", fmt.Errorf("invalid altcha challenge hash %q", ch.Challenge)
	}

	found := -1
	for n := 0; n <= maxN; n++ {
		input := ch.Salt + strconv.Itoa(n)
		sum := sha256.Sum256([]byte(input))
		if bytes.Equal(sum[:], target) {
			found = n
			break
		}
	}
	if found < 0 {
		return "", fmt.Errorf("altcha PoW solution not found in 0..%d", maxN)
	}

	sol := altchaSolution{
		Algorithm: ch.Algorithm,
		Challenge: ch.Challenge,
		Number:    found,
		Salt:      ch.Salt,
		Signature: ch.Signature,
		Took:      time.Since(t0).Milliseconds(),
	}
	if sol.Algorithm == "" {
		sol.Algorithm = "SHA-256"
	}
	raw, err := json.Marshal(sol)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func (r *Resolver) downloadFromAnnasArchive(ctx context.Context, md5Hash string, dois []string, overrideBase string) ([]byte, string, error) {
	mirrors := defaultAnnaMirrors
	if overrideBase != "" {
		mirrors = []string{strings.TrimRight(overrideBase, "/")}
	}
	apiKey := strings.TrimSpace(os.Getenv("ANNAS_ARCHIVE_KEY"))
	envCookie := strings.TrimSpace(os.Getenv("ANNA_COOKIE"))

	for _, mirror := range mirrors {
		cookies := loadCachedCookies("anna", mirror)
		if envCookie != "" {
			for _, part := range strings.Split(envCookie, ";") {
				if eq := strings.IndexByte(part, '='); eq > 0 {
					cookies[strings.TrimSpace(part[:eq])] = strings.TrimSpace(part[eq+1:])
				}
			}
		}

		// 1. If we already have an MD5 and ANNAS_ARCHIVE_KEY, try the official Fast Download API
		if md5Hash != "" && apiKey != "" {
			if data, err := r.tryAnnaFastDownload(ctx, mirror, md5Hash, apiKey, cookies); err == nil {
				return data, "annas-archive-fast", nil
			}
		}

		// 2. Try /scidb/<doi> on Anna's Archive
		for _, doi := range dois {
			scidbURL := fmt.Sprintf("%s/scidb/%s", mirror, doi)
			body, finalU, status, err := r.fetchWithCookies(ctx, scidbURL, cookies)
			if err != nil || status != http.StatusOK {
				continue
			}
			if cleaned, ok := VerifyAndTrimPDF(body); ok {
				return cleaned, "annas-archive-scidb", nil
			}
			htmlStr := string(body)
			if pdfLink := extractPDFLinkFromHTML(htmlStr, finalU); pdfLink != "" {
				if data, _, err := r.tryFetchPDF(ctx, pdfLink, "annas-archive-scidb", map[string]string{"Cookie": formatCookieMap(cookies), "Referer": scidbURL}, false); err == nil {
					return data, "annas-archive-scidb", nil
				}
			}
			if md5Hash == "" {
				if m := reAnnaMD5Link.FindStringSubmatch(htmlStr); m != nil {
					md5Hash = m[1]
				}
			}
		}

		// 3. If we have an MD5 (either passed in or discovered), try fast download or /md5/<md5>
		if md5Hash != "" {
			if apiKey != "" {
				if data, err := r.tryAnnaFastDownload(ctx, mirror, md5Hash, apiKey, cookies); err == nil {
					return data, "annas-archive-fast", nil
				}
			}
			md5URL := fmt.Sprintf("%s/md5/%s", mirror, md5Hash)
			body, finalU, status, err := r.fetchWithCookies(ctx, md5URL, cookies)
			if err == nil && status == http.StatusOK {
				htmlStr := string(body)
				if pdfLink := extractPDFLinkFromHTML(htmlStr, finalU); pdfLink != "" {
					if data, _, err := r.tryFetchPDF(ctx, pdfLink, "annas-archive-md5", map[string]string{"Cookie": formatCookieMap(cookies), "Referer": md5URL}, false); err == nil {
						return data, "annas-archive-md5", nil
					}
				}
			}
		}
	}

	return nil, "", errors.New("Anna's Archive requires clearance cookie (ANNA_COOKIE) or ANNAS_ARCHIVE_KEY when DDoS-Guard is active")
}

func (r *Resolver) tryAnnaFastDownload(ctx context.Context, mirror, md5Hash, apiKey string, cookies map[string]string) ([]byte, error) {
	u := fmt.Sprintf("%s/dyn/api/fast_download.json?md5=%s&key=%s", mirror, url.QueryEscape(md5Hash), url.QueryEscape(apiKey))
	hdrs := map[string]string{}
	if c := formatCookieMap(cookies); c != "" {
		hdrs["Cookie"] = c
	}
	var res struct {
		DownloadURL string `json:"download_url"`
		Error       string `json:"error"`
	}
	if err := r.getJSON(ctx, u, hdrs, &res); err != nil {
		return nil, err
	}
	if res.DownloadURL == "" {
		return nil, fmt.Errorf("fast_download error: %s", res.Error)
	}
	data, _, err := r.tryFetchPDF(ctx, res.DownloadURL, "annas-archive-fast", nil, false)
	return data, err
}

func (r *Resolver) downloadFromLibgenScimag(ctx context.Context, doi string) ([]byte, string, error) {
	for _, host := range []string{"https://libgen.li", "https://libgen.la"} {
		adsURL := fmt.Sprintf("%s/ads.php?doi=%s", host, url.QueryEscape(doi))
		body, err := r.getBytes(ctx, adsURL, nil)
		if err != nil {
			continue
		}
		if m := reLibgenGetLink.FindStringSubmatch(string(body)); m != nil {
			getLink := strings.ReplaceAll(m[1], "&amp;", "&")
			if !strings.HasPrefix(getLink, "http") {
				getLink = host + "/" + strings.TrimLeft(getLink, "/")
			}
			if data, _, err := r.tryFetchPDF(ctx, getLink, "libgen-scimag", map[string]string{"Referer": adsURL}, false); err == nil {
				return data, "libgen-scimag", nil
			}
		}
	}
	return nil, "", errors.New("no valid PDF from LibGen scimag")
}

func (r *Resolver) fetchWithCookies(ctx context.Context, rawURL string, cookies map[string]string) ([]byte, *url.URL, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	if c := formatCookieMap(cookies); c != "" {
		req.Header.Set("Cookie", c)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	mergeSetCookies(cookies, resp.Header.Values("Set-Cookie"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPDFBytes))
	return body, resp.Request.URL, resp.StatusCode, err
}

func mergeSetCookies(jar map[string]string, setCookies []string) {
	for _, sc := range setCookies {
		parts := strings.Split(sc, ";")
		if len(parts) == 0 {
			continue
		}
		kv := strings.TrimSpace(parts[0])
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			jar[strings.TrimSpace(kv[:eq])] = strings.TrimSpace(kv[eq+1:])
		}
	}
}

func formatCookieMap(jar map[string]string) string {
	if len(jar) == 0 {
		return ""
	}
	parts := make([]string, 0, len(jar))
	for k, v := range jar {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func cookieCacheFile(ns string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "paperget-"+ns+"-cookies.json")
	}
	return filepath.Join(home, ".cache", "paperget", ns+"-cookies.json")
}

func loadCachedCookies(ns, origin string) map[string]string {
	out := make(map[string]string)
	data, err := os.ReadFile(cookieCacheFile(ns))
	if err != nil {
		return out
	}
	var all map[string]map[string]string
	if err := json.Unmarshal(data, &all); err != nil {
		return out
	}
	for k, v := range all[origin] {
		out[k] = v
	}
	return out
}

func saveCachedCookies(ns, origin string, cookies map[string]string) {
	if len(cookies) == 0 {
		return
	}
	path := cookieCacheFile(ns)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	all := make(map[string]map[string]string)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	all[origin] = cookies
	if raw, err := json.MarshalIndent(all, "", "  "); err == nil {
		_ = os.WriteFile(path, raw, 0o600)
	}
}
