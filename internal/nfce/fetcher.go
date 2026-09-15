package nfce

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"
)

const (
	defaultUserAgent = "meupreco/0.1 (receipt price collector)"
	maxBodyBytes     = 2 << 20 // 2 MiB, DANFE pages are tens of KiB
)

// SPSourceOptions configures the São Paulo NFC-e portal fetcher. Zero values
// fall back to sane defaults.
type SPSourceOptions struct {
	// BaseURL is the QR consultation endpoint (see SPURLBuilder). Defaults to
	// DefaultSPQRBaseURL.
	BaseURL string
	// RPS caps requests per second to the portal. Defaults to 1.
	RPS int
	// UserAgent sent with every request. Defaults to a descriptive one.
	UserAgent string
	// HTTPClient is used verbatim when set; otherwise a client with a cookie
	// jar and sane timeouts is built. Provide your own to inject a transport
	// (httptest) or tweak timeouts.
	HTTPClient *http.Client
	// Logger receives debug logs (retries, rates). Optional.
	Logger *slog.Logger
	// MaxRetries is the number of attempts per fetch (>= 1). Defaults to 3.
	MaxRetries int
	// ChallengeStore records captcha challenges for later resolution.
	// Defaults to a fresh store.
	ChallengeStore *ChallengeStore
}

// SPSource fetches raw NFC-e pages from the São Paulo portal ("qrcode" view,
// which renders the DANFE for an access key).
type SPSource struct {
	baseURL    string
	limit      *rateLimiter
	user_agent string
	client     *http.Client
	log        *slog.Logger
	retries    int
	challenges *ChallengeStore
}

func NewSPSource(opts SPSourceOptions) (*SPSource, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultSPQRBaseURL
	}

	if opts.RPS < 1 {
		opts.RPS = 1
	}

	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}

	if opts.MaxRetries < 1 {
		opts.MaxRetries = 3
	}

	if opts.ChallengeStore == nil {
		opts.ChallengeStore = NewChallengeStore()
	}

	if opts.HTTPClient == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("nfce: build cookie jar: %w", err)
		}

		opts.HTTPClient = &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
				TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			},
			Timeout: 30 * time.Second,
		}
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &SPSource{
		baseURL:    opts.BaseURL,
		limit:      newRateLimiter(opts.RPS),
		user_agent: opts.UserAgent,
		client:     opts.HTTPClient,
		log:        opts.Logger,
		retries:    opts.MaxRetries,
		challenges: opts.ChallengeStore,
	}, nil
}

// Fetch retrieves the DANFE document for key. The returned bytes are the raw
// HTML; parsing is a separate concern (see HTMLParser).
func (s *SPSource) Fetch(ctx context.Context, key AccessKey) ([]byte, error) {
	url := (SPURLBuilder{BaseURL: s.baseURL}).QRCodeURL(key, envProduction)

	var lastErr error
	for attempt := 1; attempt <= s.retries; attempt++ {
		if err := s.limit.Wait(ctx); err != nil {
			return nil, err
		}

		status, body, err := s.doRequest(ctx, url)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < s.retries {
				if !sleepCtx(ctx, backoff(attempt)) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, lastErr
		}

		switch {
		case status >= 500 || status == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("nfce: portal answered HTTP %d", status)
			if attempt < s.retries {
				if !sleepCtx(ctx, backoff(attempt)) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("%w: %v", ErrUpstream, lastErr)
		case status >= 400:
			return nil, fmt.Errorf("%w: portal answered HTTP %d", ErrUpstream, status)
		}

		if err := pageFailure(body); err != nil {
			capErr, asErr := errors.AsType[*CaptchaError](err)
			if asErr {
				ch := s.challenges.Register(capErr.ImageURL, body)
				capErr.ChallengeID = ch.ID
				return nil, capErr
			}
			return nil, err
		}
		return body, nil
	}

	return nil, func() error {
		if lastErr == nil {
			return fmt.Errorf("%w: no attempts made", ErrUpstream)
		}
		return fmt.Errorf("%w: %v", ErrUpstream, lastErr)
	}()
}

func (source *SPSource) doRequest(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", source.user_agent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9")

	resp, err := source.client.Do(req)
	if err != nil {
		return 0, nil, err
	}

	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// pageFailure returns a typed error whenever body is not a healthy DANFE page.
func pageFailure(body []byte) error {
	if len(normalizeWS(string(body))) < 100 {
		return fmt.Errorf("%w: empty or truncated portal response", ErrUpstream)
	}
	if pageHasDANFE(body) {
		return nil
	}
	if looksLikeCaptcha(body) {
		return &CaptchaError{
			ImageURL: extractCaptchaImage(body),
		}
	}
	lower := strings.ToLower(string(body))

	switch {
	case strings.Contains(lower, "localizad"),
		strings.Contains(lower, "não encontrad"),
		strings.Contains(lower, "nao encontrad"):
		return fmt.Errorf("%w: document not found", ErrKeyNotFound)
	}
	return fmt.Errorf("%w: consulted page contains no DANFE", ErrBadResponse)
}

var (
	captchaRegex = regexp.MustCompile(`(?i)(<img[^>]+(?:src|alt)=\s*"[^"]*captcha[^"]*"|id=\s*"[^"]*[cC]aptcha[^"]*"|name=\s*"txtCodigo"|id=\s*"txtCodigo")`)
	imgSrcRegex  = regexp.MustCompile(`(?i)<img[^>]+src="([^"]+)"`)
)

// looksLikeCaptcha reports true when the page is the ASP.NET challenge form
// (viewstate present) and carries a captcha marker (captcha image or a code
// input). The DANFE result page also carries viewstate, so the captcha marker
// is what distinguishes the two.
func looksLikeCaptcha(body []byte) bool {
	if !strings.Contains(string(body), "__VIEWSTATE") {
		return false
	}
	return captchaRegex.Match(body)
}

// extractCaptchaImage pulls the challenge image URL out of a captcha page.
func extractCaptchaImage(body []byte) string {
	s := string(body)
	if !captchaRegex.MatchString(s) {
		return ""
	}

	for _, m := range imgSrcRegex.FindAllStringSubmatch(s, -1) {
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// pageHasDANFE reports whether body contains the DANFE result markers used by
// the São Paulo "Consulta Resumida" layout.
func pageHasDANFE(body []byte) bool {
	s := string(body)
	return strings.Contains(s, `id="tabResult"`) ||
		strings.Contains(s, "DOCUMENTO AUXILIAR DA NOTA FISCAL DE CONSUMIDOR") ||
		strings.Contains(s, "Consulta Resumida NFC-e")
}

// backoff returns an exponential delay with jitter for a retry attempt
// (attempt counting from 1). Caps at 2s.
func backoff(attempt int) time.Duration {
	d := 200 * time.Millisecond << (attempt - 1)
	if attempt > 4 {
		d = 2 * time.Second
	}
	d += time.Duration(rand.Int64N(int64(d)/4 + 1))
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// normalizeWS collapses every run of whitespace (including nbsp) into a
// single space and trims the result.
func normalizeWS(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u00a0'
	}), " ")
}
