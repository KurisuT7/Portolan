package store

import (
	"context"
	"database/sql"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// commitSync makes the resource mutation and its executable snapshot visible
// together. A failed snapshot or queue write rolls back the caller's mutation.
func (s *Store) commitSync(ctx context.Context, tx *sql.Tx, serverID string) error {
	if err := s.enqueueSyncTx(ctx, tx, serverID); err != nil {
		return err
	}
	return s.commitAndNotify(tx, []string{serverID})
}

func (s *Store) commitAndNotify(tx *sql.Tx, servers []string) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, serverID := range servers {
		s.notifyJobs(serverID)
	}
	return nil
}

// WatchJobs must be called before checking the queue, so an enqueue between the
// check and the wait cannot be missed. The caller releases its registration on
// every exit, including timeout and disconnect.
func (s *Store) WatchJobs(serverID string) (<-chan struct{}, func()) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.waiters == nil {
		s.waiters = make(map[string]map[chan struct{}]struct{})
	}
	if s.waiters[serverID] == nil {
		s.waiters[serverID] = make(map[chan struct{}]struct{})
	}
	changed := make(chan struct{})
	s.waiters[serverID][changed] = struct{}{}
	return changed, func() {
		s.wakeMu.Lock()
		defer s.wakeMu.Unlock()
		delete(s.waiters[serverID], changed)
		if len(s.waiters[serverID]) == 0 {
			delete(s.waiters, serverID)
		}
	}
}

func (s *Store) notifyJobs(serverID string) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	for changed := range s.waiters[serverID] {
		close(changed)
	}
	delete(s.waiters, serverID)
}
