package state

import "sync"

const (
	StepIdle            = "idle"
	StepWaitingLookup   = "waiting_lookup"
	StepWaitingMinPrice = "waiting_min_price"
	StepWaitingMaxPrice = "waiting_max_price"
	StepEditingMinPrice = "editing_min_price"
	StepEditingMaxPrice = "editing_max_price"
)

// Session holds the in-progress dialog state for one Telegram chat.
type Session struct {
	Step         string
	LookupID     string // set during StepWaitingLookup
	ProductName  string // filled after lookup completes
	ProductURL   string
	CurrentPrice float64
	MinPrice     float64
	// edit flow
	EditingSubID string
	OldMinPrice  float64
	OldMaxPrice  float64
	Page         int
}

// Store is a thread-safe in-memory session store.
type Store struct {
	mu       sync.Mutex
	sessions map[int64]Session
}

func New() *Store {
	return &Store{sessions: make(map[int64]Session)}
}

func (s *Store) Get(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[chatID]; ok {
		copy := sess
		return &copy
	}
	return &Session{Step: StepIdle}
}

func (s *Store) Set(chatID int64, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess == nil {
		delete(s.sessions, chatID)
		return
	}
	s.sessions[chatID] = *sess
}

// Update applies fn while holding the store lock. The callback must not call
// back into this Store.
func (s *Store) Update(chatID int64, fn func(*Session)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[chatID]
	if !ok {
		sess.Step = StepIdle
	}
	fn(&sess)
	if sess.Step == StepIdle && sess.LookupID == "" && sess.EditingSubID == "" && sess.Page == 0 {
		delete(s.sessions, chatID)
		return
	}
	s.sessions[chatID] = sess
}

// CompleteLookup replaces only the active waiting session for lookupID.
func (s *Store) CompleteLookup(chatID int64, lookupID string, result Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[chatID]
	if !ok || current.Step != StepWaitingLookup || current.LookupID != lookupID {
		return false
	}
	result.Page = current.Page
	s.sessions[chatID] = result
	return true
}

func (s *Store) Clear(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, chatID)
}
