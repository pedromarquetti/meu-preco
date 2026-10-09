package nfce

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// defaultChallengeTTL is how long an unsolved challenge may live before it
	// is dropped (the SEFAZ session behind it dies too).
	defaultChallengeTTL = 5 * time.Minute
	// defaultChallengeMaxAttempts caps the wrong answers a single challenge
	// tolerates before it is locked.
	defaultChallengeMaxAttempts = 5
)

// Challenge is the full state needed to replay a SEFAZ captcha solve later:
// the session the fetch attempt happened in (cookies + hidden ASP.NET fields),
// the form action that receives the answer POST, the image to show the user,
// and the attempt budget. It is created and registered by the Source when the
// portal answers with a challenge and consumed exactly once by a successful
// solve. The solution text itself is never stored here.
type Challenge struct {
	ID          string
	AccessKey   string            // receipt the challenge guards (for tracing)
	Payload     string            // p= context the fetch used (for tracing)
	ImageURL    string            // absolute URL of the challenge image
	FormAction  string            // absolute URL that receives the answer POST
	Fields      map[string]string // hidden inputs to replay (__VIEWSTATE, ...)
	Cookies     []*http.Cookie    // session snapshot (what the portal set)
	Raw         []byte            // challenge page as received (contains no solution)
	MaxAttempts int
	Attempts    int
	Solved      bool
	TTL         time.Time
}

// AttemptsLeft is how many wrong answers remain before the challenge locks.
func (c *Challenge) AttemptsLeft() int {
	left := c.MaxAttempts - c.Attempts

	if left < 0 {
		left = 0
	}

	return left
}

// ChallengeStore is a small in-memory registry of pending captcha challenges.
// Every access is guarded by a mutex; expired entries are dropped lazily so
// the map cannot grow without bound while a portal keeps challenging.
type ChallengeStore struct {
	mu   sync.Mutex
	next map[string]*Challenge
	now  func() time.Time
	ttl  time.Duration
}

// NewChallengeStore returns an empty challenge registry.
func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		next: make(map[string]*Challenge),
		now:  time.Now,
		ttl:  defaultChallengeTTL,
	}
}

func randomHex(n int) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:n/2])
}

func (s *ChallengeStore) newID() string {
	var id string
	for id == "" {
		id = randomHex(16)
	}
	return id
}

// Register stores a challenge, assigning it a fresh random ID, a default TTL
// and attempt budget (both overridable on the struct). Old expired challenges
// are swept on the way in to keep the map bounded.
func (s *ChallengeStore) Register(c *Challenge) Challenge {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultChallengeMaxAttempts
	}
	if c.TTL.IsZero() {
		c.TTL = s.now().Add(s.ttl)
	}
	c.ID = s.newID()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	s.next[c.ID] = c
	return *c
}

// Get returns a shallow copy of a pending, unexpired, unsolved challenge.
func (s *ChallengeStore) Get(id string) (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookupLocked(id)
	if err != nil {
		return Challenge{}, err
	}
	return *c, nil
}

// Status is the read-only state view used by the GET /captcha handler: unlike
// Get/Begin it tolerates a solved challenge (the caller reports the "solved"
// state) while still rejecting a missing or expired one.
func (s *ChallengeStore) Status(id string) (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.next[id]
	if !ok {
		return Challenge{}, ErrChallengeNotFound
	}
	if s.now().After(c.TTL) {
		delete(s.next, id)
		return Challenge{}, ErrChallengeExpired
	}
	return *c, nil
}

// Begin loads a challenge for one more solve attempt. A solved or exhausted
// challenge refuses to begin again (ErrChallengeReused / ErrChallengeLocked).
func (s *ChallengeStore) Begin(id string) (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookupLocked(id)
	if err != nil {
		return Challenge{}, err
	}
	if c.AttemptsLeft() <= 0 {
		return Challenge{}, ErrChallengeLocked
	}
	return *c, nil
}

// RecordSuccess marks a challenge solved (single-use). A second success on the
// same id is rejected so a replayed solve answers 409, not silent success.
// The retained solved row is swept by TTL later.
func (s *ChallengeStore) RecordSuccess(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookupLocked(id)
	if err != nil {
		return err
	}
	c.Solved = true
	return nil
}

// RecordFailure increments a challenge's wrong-answer count and refreshes its
// session tokens (hidden fields, raw page) from the freshly served challenge
// page a wrong solve returns, so a retry posts the viewstate the portal now
// expects. The next Begin rejects the challenge once the budget is spent.
func (s *ChallengeStore) RecordFailure(id string, fresh []byte) (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookupLocked(id)
	if err != nil {
		return Challenge{}, err
	}
	c.Attempts++
	if len(fresh) > 0 {
		p := parseCaptchaPage(fresh)
		if len(p.fields) > 0 {
			c.Fields = p.fields
		}
		c.Raw = fresh
	}
	return *c, nil
}

// Count reports the number of challenges currently held (tests, metrics).
func (s *ChallengeStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	return len(s.next)
}

// lookupLocked resolves and validates a challenge. The caller holds mu.
func (s *ChallengeStore) lookupLocked(id string) (*Challenge, error) {
	c, ok := s.next[id]
	if !ok {
		return nil, ErrChallengeNotFound
	}
	if s.now().After(c.TTL) {
		delete(s.next, id)
		return nil, ErrChallengeExpired
	}
	if c.Solved {
		return nil, ErrChallengeReused
	}
	return c, nil
}

// sweepLocked drops expired challenges. The caller holds mu.
func (s *ChallengeStore) sweepLocked(now time.Time) {
	for id, c := range s.next {
		if now.After(c.TTL) {
			delete(s.next, id)
		}
	}
}

// captchaPage is the extractable content of an ASP.NET captcha challenge page:
// the challenge image, the postback form action, and the hidden inputs
// (__VIEWSTATE, __VIEWSTATEGENERATOR, __EVENTVALIDATION) that a solve replay
// must resubmit alongside the typed answer.
type captchaPage struct {
	imageSrc   string
	formAction string
	fields     map[string]string
}

var (
	formActionRegex = regexp.MustCompile(`(?i)<form\b[^>]*\baction\s*=\s*"([^"]+)"`)
	inputTagRegex   = regexp.MustCompile(`(?i)<input\b[^>]*>`)
	fieldKeyRegex   = regexp.MustCompile(`(?i)\bname\s*=\s*"([^"]+)"`)
	fieldValRegex   = regexp.MustCompile(`(?i)\bvalue\s*=\s*"([^"]*)"`)
)

// parseCaptchaPage pulls the replay context out of the challenge form. It
// tolerates a missing form action (some portals render a bare image) but
// always captures every hidden field it can read.
func parseCaptchaPage(body []byte) captchaPage {
	s := string(body)
	page := captchaPage{fields: make(map[string]string)}

	if m := formActionRegex.FindStringSubmatch(s); len(m) > 1 {
		page.formAction = m[1]
	}

	page.imageSrc = extractCaptchaImage([]byte(s))

	for _, tag := range inputTagRegex.FindAllString(s, -1) {
		if !isHiddenInput(tag) {
			continue
		}
		name := attr(tag, fieldKeyRegex)
		if name == "" {
			continue
		}
		page.fields[name] = attr(tag, fieldValRegex)
	}

	return page
}

func isHiddenInput(tag string) bool {
	l := strings.ToLower(tag)
	return strings.Contains(l, "type=") && strings.Contains(l, "hidden")
}

func attr(tag string, re *regexp.Regexp) string {
	if m := re.FindStringSubmatch(tag); len(m) > 1 {
		return m[1]
	}
	return ""
}
