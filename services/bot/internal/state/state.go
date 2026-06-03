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
	LookupID     string  // set during StepWaitingLookup
	ProductName  string  // filled after lookup completes
	ProductURL   string
	CurrentPrice float64
	MinPrice     float64
	// edit flow
	EditingSubID string
	OldMinPrice  float64
	OldMaxPrice  float64
}

// Store is a thread-safe in-memory session store.
type Store struct {
	mu       sync.Mutex
	sessions map[int64]*Session
}

func New() *Store {
	return &Store{sessions: make(map[int64]*Session)}
}

func (s *Store) Get(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[chatID]; ok {
		return sess
	}
	return &Session{Step: StepIdle}
}

func (s *Store) Set(chatID int64, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[chatID] = sess
}

func (s *Store) Clear(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, chatID)
}
