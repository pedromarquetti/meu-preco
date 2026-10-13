package nfce

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CaptchaSolver replays a user-typed solution against the SEFAZ portal in the
// session a Challenge captured. The portal owns the stateful round trip (cookies
// + viewstate); this type just speaks ASP.NET to it.
type CaptchaSolver interface {
	// Solve POSTs the typed solution back to the portal in the challenge's
	// session. A correct answer returns the DANFE page with a nil error; a
	// rejected answer returns ErrWrongSolution along with the fresh challenge
	// page in raw, so the caller can refresh the stored session tokens for the
	// next attempt.
	Solve(ctx context.Context, ch *Challenge, solution string) (raw []byte, err error)
	// Image fetches the challenge image from the portal, returning the raw
	// bytes and content type so the server can proxy them (never storing them).
	Image(ctx context.Context, ch *Challenge) (body []byte, contentType string, err error)
}

// HTTPChallengeSolver is the endpoint implementation of CaptchaSolver: it
// posts the answer to the challenge's form action using the challenge's cookie
// snapshot and hidden fields.
type HTTPChallengeSolver struct {
	client    *http.Client
	log       *slog.Logger
	userAgent string
	limit     *rateLimiter
}

// HTTPChallengeSolverOptions configures the solver. Zero values fall back to
// sane defaults.
// Private keys for this one only: HTTPClient/UserAgent/Logger/RPS plus a
// Timeout knob mirroring the fetcher's portal budget.
type HTTPChallengeSolverOptions struct {
	// HTTPClient is used verbatim when set (inject a transport for tests).
	HTTPClient *http.Client
	// UserAgent sent with every portal request.
	UserAgent string
	// Logger receives debug logs. Optional.
	Logger *slog.Logger
	// RPS caps portal requests per second. Defaults to 1 (shared politeness).
	RPS int
	// Timeout bounds each solve/image request. Defaults to 30s.
	Timeout time.Duration
}

// NewHTTPChallengeSolver builds a solver sharing the portal's polite pacing.
func NewHTTPChallengeSolver(opts HTTPChallengeSolverOptions) *HTTPChallengeSolver {
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	if opts.HTTPClient == nil {
		if opts.Timeout <= 0 {
			opts.Timeout = 30 * time.Second
		}
		opts.HTTPClient = &http.Client{Timeout: opts.Timeout}
	}
	if opts.RPS < 1 {
		opts.RPS = 1
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &HTTPChallengeSolver{
		client:    opts.HTTPClient,
		log:       opts.Logger,
		userAgent: opts.UserAgent,
		limit:     newRateLimiter(opts.RPS),
	}
}

// Solve posts txtCodigo (the typed answer) plus the challenge's hidden fields
// to the portal form action, reusing the captured session cookies.
func (s *HTTPChallengeSolver) Solve(ctx context.Context, ch *Challenge, solution string) ([]byte, error) {
	if ch == nil || ch.FormAction == "" {
		return nil, ErrBadResponse
	}

	form := url.Values{}
	for k, v := range ch.Fields {
		form.Set(k, v)
	}
	form.Set("txtCodigo", solution)
	form.Set("btnProsseguir", "Prosseguir")

	if err := s.limit.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.FormAction, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	for _, c := range ch.Cookies {
		req.AddCookie(c)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: portal answered HTTP %d on captcha solve", ErrUpstream, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: portal answered HTTP %d on captcha solve", ErrUpstream, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	switch failure := pageFailure(body).(type) {
	case nil:
		return body, nil // the DANFE page: solved
	case *CaptchaError:
		return body, ErrWrongSolution
	default:
		if errors.Is(failure, ErrKeyNotFound) {
			return nil, fmt.Errorf("%w: captcha accepted but receipt not found", ErrKeyNotFound)
		}
		return nil, failure
	}
}

// Image proxies the challenge image bytes from the portal (with the captured
// session cookies) back to the caller. The bytes are handed to the client, not
// stored anywhere.
func (s *HTTPChallengeSolver) Image(ctx context.Context, ch *Challenge) ([]byte, string, error) {
	if ch == nil || ch.ImageURL == "" {
		return nil, "", ErrBadResponse
	}

	if err := s.limit.Wait(ctx); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ch.ImageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", s.userAgent)
	for _, c := range ch.Cookies {
		req.AddCookie(c)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("%w: image answered HTTP %d", ErrUpstream, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	ctype := resp.Header.Get("Content-Type")
	if ctype == "" {
		ctype = http.DetectContentType(body)
	}
	return body, ctype, nil
}
