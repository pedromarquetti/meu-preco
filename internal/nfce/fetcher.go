package nfce

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultUserAgent = "MeuPreco/0.1"
	maxBodyBytes     = 2 << 20 // 2 MiB, DANFE pages are tens of KiB

	// maxPortalCooldown bounds both a configured cooldown and a hostile or
	// broken Retry-After header. A bad header must never park every request
	// for an arbitrary amount of time.
	maxPortalCooldown = 15 * time.Minute
)

// HTTPSourceOptions configures a fiscal-portal fetcher. Zero values fall back
// to sane defaults.
type HTTPSourceOptions struct {
	// BaseURL is the QR consultation endpoint (see QRBuilder). Defaults to the
	// São Paulo production base, which is what a resolver wired from the
	// registry passes in.
	BaseURL string
	// RPS (Requests Per Second) caps requests per second to the portal. Defaults to 1.
	RPS int
	// MinInterval is the minimum spacing between two portal requests. It is
	// applied on top of RPS (whichever is slower wins). Defaults to 0; the
	// production config sets SEFAZ_MIN_INTERVAL because the SEFAZ edge blocks
	// a source after a couple of rapid requests.
	MinInterval time.Duration
	// Limiter is the shared portal pacing/cooldown gate. When set, RPS and
	// MinInterval are ignored: the caller owns one limiter per portal host and
	// also injects it into the captcha solver, so a rejection on either path
	// stops the other.
	Limiter *PortalLimiter
	// UserAgent sent with every request. Defaults to a descriptive one.
	UserAgent string
	// Cookie is an optional raw Cookie header used only by the diagnostic CLI
	// to replay a browser session (Phase 7 experiment E4). It is never wired
	// from the server config and never logged.
	Cookie string
	// HTTPClient is used verbatim when set; otherwise a client with a cookie
	// jar and sane timeouts is built. Provide your own to inject a transport
	// (httptest) or tweak timeouts.
	HTTPClient *http.Client
	// Logger receives debug logs (retries, rates). Optional.
	Logger *slog.Logger
	// MaxRetries is the number of attempts per fetch (>= 1). Defaults to 1: a
	// challenge-less request to the SEFAZ edge is dropped at the network level,
	// and retrying it immediately only amplifies the source block.
	MaxRetries int
	// Timeout bounds each portal request (including retries of the same
	// attempt sequence). Defaults to 30s.
	Timeout time.Duration
	// BackoffBase is the exponential retry delay for the first retry. Defaults
	// to 200ms.
	BackoffBase time.Duration
	// BackoffCap caps every exponential retry delay. Defaults to 2s. Values
	// below BackoffBase make the backoff flat (always at the cap).
	BackoffCap time.Duration
	// Cooldown is how long subsequent requests wait after the portal serves an
	// anti-bot/rate-limit rejection page. Defaults to 1 minute.
	Cooldown time.Duration
	// ChallengeStore records captcha challenges for later resolution.
	// Defaults to a fresh store.
	ChallengeStore *ChallengeStore
}

// HTTPSource fetches raw NFC-e pages from a state fiscal portal that renders
// the DANFE through its "qrcode" view for an access key or QR payload.
type HTTPSource struct {
	baseURL     string
	limit       *PortalLimiter
	user_agent  string
	cookie      string
	cookieSeed  sync.Once
	client      *http.Client
	log         *slog.Logger
	retries     int
	backoffBase time.Duration
	backoffCap  time.Duration
	cooldown    time.Duration
	challenges  *ChallengeStore
}

func NewHTTPSource(opts HTTPSourceOptions) (*HTTPSource, error) {
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
		opts.MaxRetries = 1
	}

	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	if opts.BackoffBase < 0 {
		opts.BackoffBase = 0
	}
	if opts.BackoffCap <= 0 {
		opts.BackoffCap = 2 * time.Second
	}
	if opts.BackoffBase == 0 {
		opts.BackoffBase = 200 * time.Millisecond
	}
	if opts.BackoffCap < opts.BackoffBase {
		opts.BackoffCap = opts.BackoffBase
	}

	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Minute
	}
	opts.Cooldown = capCooldown(opts.Cooldown)

	if opts.ChallengeStore == nil {
		opts.ChallengeStore = NewChallengeStore()
	}

	if opts.Limiter == nil {
		opts.Limiter = NewPortalLimiter(opts.RPS, opts.MinInterval)
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
			Timeout: opts.Timeout,
		}
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &HTTPSource{
		baseURL:     opts.BaseURL,
		limit:       opts.Limiter,
		user_agent:  opts.UserAgent,
		cookie:      opts.Cookie,
		client:      opts.HTTPClient,
		log:         opts.Logger,
		retries:     opts.MaxRetries,
		backoffBase: opts.BackoffBase,
		backoffCap:  opts.BackoffCap,
		cooldown:    opts.Cooldown,
		challenges:  opts.ChallengeStore,
	}, nil
}

// Fetch retrieves the DANFE document for key. When payload is non-empty it
// replays that exact QR payload (see ParseQRPayload) against the portal,
// which is what lets the server honor the URL the consumer actually scanned.
// The returned bytes are raw HTML; parsing is a separate concern (see
// HTMLParser).
func (s *HTTPSource) Fetch(ctx context.Context, key AccessKey, payload string) ([]byte, error) {
	builder := QRBuilder{BaseURL: s.baseURL}
	requestURL := builder.FromKey(key, envProduction)
	if payload != "" {
		requestURL = builder.FromPayload(payload)
	}
	host, path := requestTarget(requestURL)

	s.log.Debug("nfce: portal fetch start",
		"access_key", key.String(),
		"host", host,
		"path", path,
		"user_agent", s.user_agent,
		"max_retries", s.retries,
	)

	var lastErr error
	for attempt := 1; attempt <= s.retries; attempt++ {
		waitStarted := time.Now()
		if err := s.limit.Wait(ctx); err != nil {
			s.log.Debug("nfce: portal rate limiter wait failed",
				"access_key", key.String(),
				"attempt", attempt,
				"err", err,
			)
			return nil, err
		}
		if waited := time.Since(waitStarted); waited > 0 {
			s.log.Debug("nfce: portal limiter wait",
				"access_key", key.String(),
				"attempt", attempt,
				"wait_ms", waited.Milliseconds(),
			)
		}

		attemptStarted := time.Now()
		s.log.Debug("nfce: portal request attempt",
			"access_key", key.String(),
			"attempt", attempt,
			"host", host,
			"path", path,
			"user_agent", s.user_agent,
		)

		status, header, body, err := s.doRequest(ctx, requestURL, key)
		duration := time.Since(attemptStarted)

		// A response may arrive together with a body-read error (a transfer
		// truncated mid-body). Classify the response first, then the transfer
		// failure: an incomplete body with a known HTTP status is not a
		// connection-layer edge drop.
		if status != 0 {
			s.log.Debug("nfce: portal response received",
				"access_key", key.String(),
				"attempt", attempt,
				"status", status,
				"bytes", len(body),
				"duration_ms", duration.Milliseconds(),
			)
			if err != nil {
				s.log.Debug("nfce: portal response body failed",
					"access_key", key.String(),
					"attempt", attempt,
					"status", status,
					"bytes", len(body),
					"err", err,
				)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}

			// Order matters: an explicit block page or JS challenge wins over
			// the status code, then HTTP status wins over body truncation.
			if blockErr := s.classifyPortalBlock(key, status, header, body, attempt, err == nil); blockErr != nil {
				return nil, blockErr
			}

			switch {
			case status == http.StatusTooManyRequests:
				// The portal explicitly asked us to wait. Record the shared
				// cooldown and fail fast instead of retrying into it: sleeping
				// through Retry-After in the handler would exceed the server's
				// write timeout and the caller would see a reset, not a 429.
				cooldown := s.cooldown
				if retryAfter, ok := parseRetryAfter(header, time.Now()); ok {
					cooldown = retryAfter
				}
				s.applyCooldown("http_429", cooldown, attempt, status, len(body), "")
				if err := s.limit.cooldownError(); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("%w: portal answered HTTP %d", ErrUpstream, status)
			case status >= 500:
				lastErr = fmt.Errorf("nfce: portal answered HTTP %d", status)
				if err != nil {
					lastErr = fmt.Errorf("nfce: portal answered HTTP %d with an incomplete body: %v", status, err)
				}
				if attempt < s.retries {
					delay := s.backoff(attempt)
					s.log.Debug("nfce: portal HTTP retry scheduled",
						"access_key", key.String(),
						"attempt", attempt,
						"next_attempt", attempt+1,
						"status", status,
						"backoff_ms", delay.Milliseconds(),
					)
					if !sleepCtx(ctx, delay) {
						return nil, ctx.Err()
					}
					continue
				}
				return nil, fmt.Errorf("%w: %v", ErrUpstream, lastErr)
			case status >= 400:
				return nil, fmt.Errorf("%w: portal answered HTTP %d", ErrUpstream, status)
			}

			if err != nil {
				// A 2xx/3xx response whose body ended early is a truncated
				// upstream response, not an edge drop. Keep the status and the
				// partial byte count for diagnostics.
				return nil, fmt.Errorf("%w: portal response truncated after %d bytes (status %d): %v",
					ErrUpstream, len(body), status, err)
			}
		} else if err != nil {
			lastErr = err
			s.log.Debug("nfce: portal request failed",
				"access_key", key.String(),
				"attempt", attempt,
				"duration_ms", duration.Milliseconds(),
				"err", err,
			)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if isEdgeDrop(err) {
				// The SEFAZ edge drops challenge-less clients at the network
				// layer (TCP reset / EOF) before any HTTP response. Retrying
				// immediately is exactly what the anti-bot policy penalises,
				// so classify it as a block and let the shared cooldown apply.
				s.log.Warn("nfce: portal edge drop classified as anti-bot block",
					"access_key", key.String(),
					"attempt", attempt,
					"duration_ms", duration.Milliseconds(),
					"err", err,
				)
				s.applyCooldown("edge_drop", s.cooldown, attempt, 0, 0, "")
				return nil, &AntiBotError{
					Reason:     "connection dropped by the portal edge",
					RetryAfter: s.cooldown,
				}
			}
			if isConnectionDrain(err) {
				// A GOAWAY or closed connection is a transport condition, not
				// an anti-bot block. Drop idle connections so a permitted retry
				// opens a fresh one instead of reusing the drained socket.
				s.client.CloseIdleConnections()
				s.log.Debug("nfce: portal connection drained; retrying on a fresh connection",
					"access_key", key.String(),
					"attempt", attempt,
					"err", err,
				)
			}
			if attempt < s.retries {
				delay := s.backoff(attempt)
				s.log.Debug("nfce: portal retry scheduled",
					"access_key", key.String(),
					"attempt", attempt,
					"next_attempt", attempt+1,
					"backoff_ms", delay.Milliseconds(),
				)
				if !sleepCtx(ctx, delay) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("%w: %v", ErrUpstream, lastErr)
		}

		if err := pageFailure(body); err != nil {
			s.log.Debug("nfce: portal page rejected",
				"access_key", key.String(),
				"attempt", attempt,
				"status", status,
				"bytes", len(body),
				"err", err,
			)
			capErr, asErr := errors.AsType[*CaptchaError](err)
			if asErr {
				ch := s.captureChallenge(requestURL, payload, key, capErr.ImageURL, body)
				capErr.ChallengeID = ch.ID
				return nil, capErr
			}
			return nil, err
		}
		s.log.Debug("nfce: portal DANFE accepted",
			"access_key", key.String(),
			"attempt", attempt,
			"bytes", len(body),
		)
		return body, nil
	}

	return nil, func() error {
		if lastErr == nil {
			return fmt.Errorf("%w: no attempts made", ErrUpstream)
		}
		return fmt.Errorf("%w: %v", ErrUpstream, lastErr)
	}()
}

// applyCooldown records a shared cooldown and logs the safe transition fields
// (never the response body).
func (s *HTTPSource) applyCooldown(reason string, d time.Duration, attempt, status, bodyBytes int, supportID string) {
	d = capCooldown(d)
	if d <= 0 {
		return
	}

	until, changed := s.limit.Cooldown(d)
	attrs := []any{
		"reason", reason,
		"duration_ms", d.Milliseconds(),
		"cooldown_until", until,
		"attempt", attempt,
		"status", status,
		"bytes", bodyBytes,
	}
	if supportID != "" {
		attrs = append(attrs, "support_id", supportID)
	}

	if changed {
		s.log.Warn("nfce: portal cooldown applied", attrs...)
		return
	}
	s.log.Debug("nfce: portal cooldown already active", attrs...)
}

// classifyPortalBlock returns a typed anti-bot error when the response is an
// explicit rejection page, an F5 JavaScript challenge, an HTTP 403 block, or a
// blank HTTP 200 block page. It applies the shared cooldown before returning,
// so callers must stop immediately and never retry into it.
//
// bodyComplete is false when the response body ended early: a partial body can
// still contain a challenge/rejection marker, but a short partial body must be
// reported as truncated rather than as a blank block page.
func (s *HTTPSource) classifyPortalBlock(key AccessKey, status int, header http.Header, body []byte, attempt int, bodyComplete bool) error {
	if pageHasDANFE(body) {
		return nil
	}

	if supportID, rejected := antiBotRejection(body); rejected {
		cooldown := s.cooldown
		reason := "anti_bot_rejection"
		if retryAfter, ok := parseRetryAfter(header, time.Now()); ok {
			s.log.Debug("Retry After", "retry", retryAfter)
			cooldown = retryAfter
			reason = "anti_bot_rejection_retry_after"
		}
		s.applyCooldown(reason, cooldown, attempt, status, len(body), supportID)
		return &AntiBotError{
			StatusCode: status,
			SupportID:  supportID,
			BodyBytes:  len(body),
			RetryAfter: cooldown,
		}
	}

	// F5/BIG-IP JavaScript challenges are a distinct block: unlike the
	// explicit rejection page, the portal expects the client to run a script.
	// This server does not, and never will. Classify it, apply the shared
	// cooldown, and stop: retrying or "solving" it only deepens the block.
	if marker, challenged := antiBotChallenge(body); challenged {
		s.log.Warn("nfce: portal JavaScript challenge classified as anti-bot block",
			"access_key", key.String(),
			"attempt", attempt,
			"status", status,
			"bytes", len(body),
			"marker", marker,
		)
		s.applyCooldown("js_challenge", s.cooldown, attempt, status, len(body), "")
		return &AntiBotChallengeError{
			StatusCode: status,
			BodyBytes:  len(body),
			Reason:     "F5/BIG-IP JavaScript challenge",
			RetryAfter: s.cooldown,
		}
	}

	if status == http.StatusForbidden {
		cooldown := s.cooldown
		if retryAfter, ok := parseRetryAfter(header, time.Now()); ok {
			cooldown = retryAfter
		}
		s.applyCooldown("http_403", cooldown, attempt, status, len(body), "")
		return &AntiBotError{
			StatusCode: status,
			BodyBytes:  len(body),
			Reason:     "HTTP 403 block page",
			RetryAfter: cooldown,
		}
	}

	if bodyComplete && status == http.StatusOK && len(normalizeWS(string(body))) < 100 {
		// A blank or near-empty 200 is the other shape of the same edge
		// block: there is no document to parse and no retry is useful.
		cooldown := s.cooldown
		if retryAfter, ok := parseRetryAfter(header, time.Now()); ok {
			cooldown = retryAfter
		}
		s.applyCooldown("empty_200", cooldown, attempt, status, len(body), "")
		return &AntiBotError{
			StatusCode: status,
			BodyBytes:  len(body),
			Reason:     "empty HTTP 200 block page",
			RetryAfter: cooldown,
		}
	}

	return nil
}

// portalTrace captures safe transport facts for one portal request. It never
// records a URL with its query string, response bodies, or cookie values.
type portalTrace struct {
	mu         sync.Mutex
	connected  bool
	reused     bool
	remoteAddr string
	tlsVersion uint16
	tlsALPN    string
	firstByte  time.Time
}

// withPortalTrace attaches a trace collector to req and returns it alongside
// the request that carries it in its context.
func withPortalTrace(req *http.Request) (*http.Request, *portalTrace) {
	trace := &portalTrace{}
	ctx := httptrace.WithClientTrace(req.Context(), trace.clientTrace())
	return req.WithContext(ctx), trace
}

func (t *portalTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.connected = true
			t.reused = info.Reused
			if info.Conn != nil {
				t.remoteAddr = info.Conn.RemoteAddr().String()
			}
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err != nil {
				return
			}
			t.mu.Lock()
			defer t.mu.Unlock()
			t.tlsVersion = state.Version
			t.tlsALPN = state.NegotiatedProtocol
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.firstByte = time.Now()
		},
	}
}

// attrs returns the safe transport fields for a debug log. first_byte_ms is -1
// when the request never produced a response byte.
func (t *portalTrace) attrs(start time.Time) []any {
	t.mu.Lock()
	defer t.mu.Unlock()

	firstByteMS := int64(-1)
	if !t.firstByte.IsZero() {
		firstByteMS = t.firstByte.Sub(start).Milliseconds()
	}
	return []any{
		"connection_established", t.connected,
		"connection_reused", t.reused,
		"remote_addr", t.remoteAddr,
		"tls_version", tlsVersionName(t.tlsVersion),
		"tls_alpn", t.tlsALPN,
		"first_byte_ms", firstByteMS,
	}
}

func tlsVersionName(version uint16) string {
	switch version {
	case 0:
		return ""
	case tls.VersionTLS10:
		return "1.0"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	default:
		return fmt.Sprintf("0x%04x", version)
	}
}

// requestTarget extracts the host and path from rawURL, deliberately dropping
// the query and fragment: a v2 QR payload lives in the query and can contain a
// digest/signature.
func requestTarget(rawURL string) (host, path string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Host, path
}

// safeHeaderAttrs returns only the response headers that are safe to log. It
// never includes Set-Cookie, Authorization, or any header that can carry a
// credential.
func safeHeaderAttrs(header http.Header) []any {
	return []any{
		"server", clipHeaderValue(header.Get("Server")),
		"content_type", clipHeaderValue(header.Get("Content-Type")),
		"via", clipHeaderValue(header.Get("Via")),
		"x_cache", clipHeaderValue(header.Get("X-Cache")),
		"retry_after", clipHeaderValue(header.Get("Retry-After")),
		"connection", clipHeaderValue(header.Get("Connection")),
	}
}

func clipHeaderValue(v string) string {
	const max = 200
	if len(v) <= max {
		return v
	}
	return v[:max] + "..."
}

// cookieNamesFrom returns only cookie names, sorted for stable logs.
func cookieNamesFrom(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c != nil {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}

// cookieAttrs returns cookie metadata (never values) for safe diagnostics.
func cookieAttrs(cookies []*http.Cookie) []string {
	attrs := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c == nil {
			continue
		}
		expires := "session"
		if !c.Expires.IsZero() {
			expires = "set"
		}
		attrs = append(attrs, fmt.Sprintf(
			"%s domain=%q path=%q expires=%s max_age=%d secure=%t http_only=%t",
			c.Name, c.Domain, c.Path, expires, c.MaxAge, c.Secure, c.HttpOnly,
		))
	}
	sort.Strings(attrs)
	return attrs
}

// hasChallengeCookie reports whether an F5 challenge cookie name appeared.
func hasChallengeCookie(names []string) bool {
	for _, name := range names {
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "TSPD") || strings.HasPrefix(upper, "BIGIPSERVER") {
			return true
		}
	}
	return false
}

// sanitizeTransportError strips query strings, fragments, and credentials from
// the URL that net/http embeds in *url.Error, recursing into nested url errors
// (http.Client wraps a transport error without removing its URL). The full QR
// payload must never reach logs or returned error strings.
func sanitizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr == nil {
		return err
	}
	urlErrCopy := *urlErr
	if u, parseErr := url.Parse(urlErr.URL); parseErr == nil {
		u.RawQuery = ""
		u.Fragment = ""
		u.User = nil
		urlErrCopy.URL = u.String()
	}
	if urlErr.Err != nil && urlErr.Err != err {
		urlErrCopy.Err = sanitizeTransportError(urlErr.Err)
	}
	return &urlErrCopy
}

func (source *HTTPSource) doRequest(ctx context.Context, rawURL string, key AccessKey) (int, http.Header, []byte, error) {
	start := time.Now()
	host, path := requestTarget(rawURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, nil, sanitizeTransportError(err)
	}
	req, trace := withPortalTrace(req)
	req.Header.Set("User-Agent", source.user_agent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9")
	if source.cookie != "" {
		// Diagnostic cookie transplant only (see HTTPSourceOptions.Cookie).
		// Seed the jar rather than setting a manual Cookie header: the client
		// appends jar cookies to any manual header, which would duplicate
		// names. A jar-less client (tests) gets the header directly. The
		// value is a session credential: never log it.
		if source.client.Jar != nil {
			// Seed once: later Set-Cookie values from the portal must win over
			// the diagnostic snapshot.
			source.cookieSeed.Do(func() {
				source.client.Jar.SetCookies(req.URL, parseCookieHeader(source.cookie))
			})
		} else {
			req.Header.Set("Cookie", source.cookie)
		}
	}

	// Record which cookies the jar would send before the request runs. Only
	// names are logged: values are session credentials, and the F5 challenge
	// cookies are signed/fingerprinted secrets.
	logCookies := source.log.Enabled(ctx, slog.LevelDebug)
	var sentNames []string
	if logCookies {
		sentNames = source.cookieNames(req.URL)
		source.log.Debug("nfce: portal request diagnostics",
			"access_key", key.String(),
			"host", host,
			"path", path,
			"cookie_count_sent", len(sentNames),
			"cookie_names_sent", sentNames,
			"challenge_cookie", hasChallengeCookie(sentNames),
		)
	}

	resp, err := source.client.Do(req)
	if err != nil {
		err = sanitizeTransportError(err)
		if logCookies {
			attrs := []any{
				"access_key", key.String(),
				"host", host,
				"path", path,
			}
			attrs = append(attrs, trace.attrs(start)...)
			attrs = append(attrs,
				"duration_ms", time.Since(start).Milliseconds(),
				"err", err,
			)
			source.log.Debug("nfce: portal transport failed", attrs...)
		}
		return 0, nil, nil, err
	}

	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if logCookies {
		setNames := cookieNamesFrom(resp.Cookies())
		allNames := append(append([]string{}, sentNames...), setNames...)
		attrs := []any{
			"access_key", key.String(),
			"host", host,
			"path", path,
			"status", resp.StatusCode,
		}
		attrs = append(attrs, trace.attrs(start)...)
		attrs = append(attrs, safeHeaderAttrs(resp.Header)...)
		attrs = append(attrs,
			"cookie_count_sent", len(sentNames),
			"cookie_names_sent", sentNames,
			"cookie_count_set", len(setNames),
			"cookie_names_set", setNames,
			"cookie_attrs_set", cookieAttrs(resp.Cookies()),
			"challenge_cookie", hasChallengeCookie(allNames),
		)
		source.log.Debug("nfce: portal response diagnostics", attrs...)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	// Preserve the status, headers and any partial bytes even when the body
	// read fails: that is what lets the caller distinguish a truncated HTTP
	// response from a connection-layer drop before any response.
	return resp.StatusCode, resp.Header, body, err
}

// parseCookieHeader turns a raw diagnostic "a=b; c=d" Cookie header into
// cookies the jar can seed. Malformed pairs are dropped; values are never
// logged.
func parseCookieHeader(raw string) []*http.Cookie {
	var cookies []*http.Cookie

	for part := range strings.SplitSeq(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: strings.TrimSpace(value)})
	}
	return cookies
}

// cookieNames returns the names (never the values) of the cookies the jar
// would attach to a request for u.
func (source *HTTPSource) cookieNames(u *url.URL) []string {
	if source.client.Jar == nil {
		return nil
	}
	return cookieNamesFrom(source.client.Jar.Cookies(u))
}

// captureChallenge registers the session context of a captcha-challenged fetch
// so the solver can replay the answer in the same session later: the absolute
// image URL, the postback form action, the hidden viewstate fields and a
// snapshot of the session cookies the challenge page was served in.
func (s *HTTPSource) captureChallenge(fetchURL, payload string, key AccessKey, imageURL string, body []byte) Challenge {
	page := parseCaptchaPage(body)
	base, _ := url.Parse(fetchURL)

	var cookies []*http.Cookie
	if s.client.Jar != nil {
		if u, err := url.Parse(fetchURL); err == nil {
			cookies = s.client.Jar.Cookies(u)
		}
	}

	return s.challenges.Register(&Challenge{
		Payload:    payload,
		AccessKey:  key.String(),
		ImageURL:   resolveURL(base, page.imageSrc, imageURL),
		FormAction: resolveURL(base, page.formAction, ""),
		Fields:     page.fields,
		Cookies:    cookies,
		Raw:        body,
	})
}

// resolveURL absolutizes a page-relative reference (form action, image src)
// against the fetch URL; empty references fall back to fallback.
func resolveURL(base *url.URL, ref, fallback string) string {
	if ref == "" {
		return fallback
	}
	if u, err := url.Parse(ref); err == nil {
		if u.IsAbs() {
			return ref
		}
		if base != nil {
			return base.ResolveReference(u).String()
		}
	}
	return ref
}

// pageFailure returns a typed error whenever body is not a healthy DANFE page.
func pageFailure(body []byte) error {
	if pageHasDANFE(body) {
		return nil
	}
	if looksLikeCaptcha(body) {
		return &CaptchaError{
			ImageURL: extractCaptchaImage(body),
		}
	}
	if _, challenged := antiBotChallenge(body); challenged {
		return &AntiBotChallengeError{
			BodyBytes: len(body),
			Reason:    "F5/BIG-IP JavaScript challenge",
		}
	}
	if len(normalizeWS(string(body))) < 100 {
		return fmt.Errorf("%w: empty or truncated portal response", ErrUpstream)
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
// (attempt counting from 1). It doubles from backoffBase every retry and caps
// at backoffCap.
func (s *HTTPSource) backoff(attempt int) time.Duration {
	d := s.backoffBase << (attempt - 1)
	if d > s.backoffCap {
		d = s.backoffCap
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

// isEdgeDrop reports whether err is the connection-level failure the SEFAZ
// anti-bot edge uses for challenge-less clients: the TCP connection is reset or
// closed before any HTTP response is produced. It deliberately excludes
// GOAWAY and closed-connection transport conditions (see isConnectionDrain):
// those are normal connection lifecycle events, not blocks. Prefer typed
// inspection; the string fallback only covers opaque wrapped errors.
func isEdgeDrop(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(opErr.Err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset by peer",
		"connection reset",
		"broken pipe",
		"unexpected eof",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// isConnectionDrain reports whether err is a transport/lifecycle condition:
// an HTTP/2 GOAWAY, or a socket the client already closed. These are not
// anti-bot blocks; a retry on a fresh connection is allowed when the retry
// budget permits.
func isConnectionDrain(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"server sent goaway",
		"use of closed network connection",
		"http2: client connection lost",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// antiBotChallenge reports whether body is an F5/BIG-IP JavaScript challenge
// page and returns the stable marker that matched. It only classifies pages
// that are neither a DANFE nor a captcha form: the DANFE result page also
// carries scripts, and a captcha is handled by its own interactive path.
//
// The markers are deliberately specific. "F5" alone is a common substring, so
// only its challenge-facing variants count. The returned marker is safe to log;
// the body itself is never logged.
func antiBotChallenge(body []byte) (string, bool) {
	if pageHasDANFE(body) || looksLikeCaptcha(body) {
		return "", false
	}

	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"/tspd",
		"tspd_101",
		"big-ip",
		"bigip",
	} {
		if strings.Contains(lower, marker) {
			return marker, true
		}
	}

	// Challenge text may be split across tags or whitespace, so match phrases
	// against a whitespace-collapsed copy.
	collapsed := normalizeWS(lower)
	for _, phrase := range []string{
		"f5 networks",
		"javascript challenge",
		"enable javascript",
		"checking your browser",
		"checking if the site connection is secure",
	} {
		if strings.Contains(collapsed, phrase) {
			return phrase, true
		}
	}
	return "", false
}

func antiBotRejection(body []byte) (string, bool) {
	s := string(body)
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "request rejected") &&
		!strings.Contains(lower, "the requested url was rejected") {
		return "", false
	}

	// SEFAZ/Akamai may render the support ID as plain text, with a colon, or
	// inside angle brackets. Return only the identifier, never the body.
	supportID := regexp.MustCompile(`(?i)support\s*id(?:\s+is)?\s*:\s*[<\[]?([[:alnum:]_-]+)`)
	if matches := supportID.FindStringSubmatch(s); len(matches) > 1 {
		return matches[1], true
	}
	return "", true
}

func parseRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	retryAfter := strings.TrimSpace(header.Get("Retry-After"))
	if retryAfter == "" {
		return 0, false
	}

	// Try parsing as seconds first.
	if sec, err := strconv.ParseInt(retryAfter, 10, 64); err == nil {
		if sec < 0 {
			return 0, false
		}
		if sec >= int64(maxPortalCooldown/time.Second) {
			return maxPortalCooldown, true
		}
		return time.Duration(sec) * time.Second, true
	} else if isDigits(retryAfter) {
		// A syntactically valid but oversized delay-seconds value overflows
		// ParseInt; it is still a huge cooldown and must be capped.
		return maxPortalCooldown, true
	}

	// Fallback to HTTP date (RFC1123/RFC850 etc.).
	t, err := http.ParseTime(retryAfter)
	if err != nil {
		return 0, false
	}
	diff := t.Sub(now)
	if diff < 0 {
		// If the date is in the past, we treat it as immediate (no wait).
		return 0, true
	}
	return capCooldown(diff), true
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// capCooldown clamps a cooldown to maxPortalCooldown. Non-positive values stay
// zero, meaning "no cooldown", and are never turned into a wait.
func capCooldown(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > maxPortalCooldown {
		return maxPortalCooldown
	}
	return d
}

// normalizeWS collapses every run of whitespace, including non-breaking
// spaces, into a single space and trims the result.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
