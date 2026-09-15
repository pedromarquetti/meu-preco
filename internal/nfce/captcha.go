package nfce

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Challenge holds everything needed to resolve a SEFAZ captcha later.
// Phase 6 extends it with the session state (cookies + viewstate) required to replay
// the answer; for now it keeps the challenge handle and the image the user
// must read.
type Challenge struct {
	ID       string
	ImageURL string
	Raw      []byte // the challenge page as received
}

// ChallengeStore is a small in-memory registry of pending captcha challenges.
// A challenge is issued on fetch and referenced by the client afterwards.
type ChallengeStore struct {
	mu   sync.RWMutex
	next map[string]Challenge
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{next: make(map[string]Challenge)}
}

func randomHex(n int) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand practically never fails; a zero id is preferable to a
		// panic but a repeated read is attempted.
		return ""
	}
	return hex.EncodeToString(b[:n/2])
}

// Register stores a challenge and returns it with a fresh random ID.
func (s *ChallengeStore) Register(imageURL string, raw []byte) Challenge {
	var id string
	for id == "" {
		id = randomHex(16)
	}

	ch := Challenge{ID: id, ImageURL: imageURL, Raw: raw}
	s.mu.Lock()
	s.next[ch.ID] = ch
	s.mu.Unlock()
	return ch
}

// Get returns a previously registered challenge.
func (s *ChallengeStore) Get(id string) (Challenge, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.next[id]
	return ch, ok
}
