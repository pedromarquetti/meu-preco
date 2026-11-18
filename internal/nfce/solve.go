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
	limit     *PortalLimiter
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
	// MinInterval is the minimum spacing between solver requests, applied on
	// top of RPS. Defaults to 0; the production config sets SEFAZ_MIN_INTERVAL.
	MinInterval time.Duration
	// Limiter is the shared portal pacing/cooldown gate. When set, RPS and
	// MinInterval are ignored: the caller injects the same limiter used by
	// the fetcher, so a fetcher rejection also stops solver requests.
	Limiter *PortalLimiter
	// Timeout bounds each solve/image request. Defaults to 30s.
	Timeout time.Duration
}

// NewHTTPChallengeSolver builds a solver sharing the portal's polite pacing.
func NewHTTPChallengeSolver(opts HTTPChallengeSolverOptions) *HTTPChallengeSolver {

	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
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
	if opts.Limiter == nil {
		opts.Limiter = NewPortalLimiter(opts.RPS, opts.MinInterval)
	}

	return &HTTPChallengeSolver{
		client:    opts.HTTPClient,
		log:       opts.Logger,
		userAgent: opts.UserAgent,
		limit:     opts.Limiter,
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
	start := time.Now()
	host, path := requestTarget(ch.FormAction)
	req, trace := withPortalTrace(req)
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	for _, c := range ch.Cookies {
		req.AddCookie(c)
	}

	sentNames := cookieNamesFrom(req.Cookies())
	s.log.Debug("nfce: portal captcha solve request",
		"host", host,
		"path", path,
		"access_key", ch.AccessKey,
		"cookie_count_sent", len(sentNames),
		"cookie_names_sent", sentNames,
		"challenge_cookie", hasChallengeCookie(sentNames),
	)

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err = sanitizeTransportError(err)
		attrs := []any{"host", host, "path", path, "access_key", ch.AccessKey}
		attrs = append(attrs, trace.attrs(start)...)
		attrs = append(attrs,
			"duration_ms", time.Since(start).Milliseconds(),
			"err", err,
		)
		s.log.Debug("nfce: portal captcha solve transport failed", attrs...)
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	setNames := cookieNamesFrom(resp.Cookies())
	attrs := []any{
		"host", host,
		"path", path,
		"access_key", ch.AccessKey,
		"status", resp.StatusCode,
	}
	attrs = append(attrs, trace.attrs(start)...)
	attrs = append(attrs, safeHeaderAttrs(resp.Header)...)
	attrs = append(attrs,
		"cookie_count_set", len(setNames),
		"cookie_names_set", setNames,
		"challenge_cookie", hasChallengeCookie(setNames),
	)
	s.log.Debug("nfce: portal captcha solve response", attrs...)

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

	// A challenge page answered with another F5/BIG-IP script challenge is not
	// a wrong captcha answer: it is a block. Log it as such without logging the
	// solution or the form fields.
	if marker, challenged := antiBotChallenge(body); challenged {
		s.log.Warn("nfce: portal captcha solve answered with a JavaScript challenge",
			"host", host,
			"path", path,
			"access_key", ch.AccessKey,
			"status", resp.StatusCode,
			"bytes", len(body),
			"marker", marker,
		)
	}

	switch failure := pageFailure(body).(type) {
	case nil:
		return body, nil // the DANFE page: solved
	case *CaptchaError:
		s.log.Debug("nfce: portal captcha answer rejected",
			"host", host,
			"path", path,
			"access_key", ch.AccessKey,
			"status", resp.StatusCode,
			"bytes", len(body),
		)
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
	start := time.Now()
	host, path := requestTarget(ch.ImageURL)
	req, trace := withPortalTrace(req)
	req.Header.Set("User-Agent", s.userAgent)
	for _, c := range ch.Cookies {
		req.AddCookie(c)
	}

	sentNames := cookieNamesFrom(req.Cookies())
	s.log.Debug("nfce: portal captcha image request",
		"host", host,
		"path", path,
		"access_key", ch.AccessKey,
		"cookie_count_sent", len(sentNames),
		"cookie_names_sent", sentNames,
		"challenge_cookie", hasChallengeCookie(sentNames),
	)

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		err = sanitizeTransportError(err)
		attrs := []any{"host", host, "path", path, "access_key", ch.AccessKey}
		attrs = append(attrs, trace.attrs(start)...)
		attrs = append(attrs,
			"duration_ms", time.Since(start).Milliseconds(),
			"err", err,
		)
		s.log.Debug("nfce: portal captcha image transport failed", attrs...)
		return nil, "", fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		attrs := []any{
			"host", host,
			"path", path,
			"access_key", ch.AccessKey,
			"status", resp.StatusCode,
			"content_type", clipHeaderValue(resp.Header.Get("Content-Type")),
		}
		attrs = append(attrs, trace.attrs(start)...)
		s.log.Debug("nfce: portal captcha image rejected", attrs...)
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

	attrs := []any{
		"host", host,
		"path", path,
		"access_key", ch.AccessKey,
		"status", resp.StatusCode,
		"content_type", clipHeaderValue(ctype),
		"bytes", len(body),
	}
	attrs = append(attrs, trace.attrs(start)...)
	s.log.Debug("nfce: portal captcha image response", attrs...)

	return body, ctype, nil
}
