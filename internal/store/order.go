package store

import (
	"context"
	"fmt"
)

// Servers and forwards are listed in the order the operator arranges in the
// console. A new row is placed after the last one; rows saved before ordering
// existed share position 0 and keep their creation order.

func nextSortOrder(table string) string {
	return "(SELECT COALESCE(MAX(sort_order),0)+1 FROM " + table + ")"
}

// ReorderServers stores the console order of all servers. ids must name every
// server exactly once, so an order arranged before another server was added
// or removed is rejected instead of misplacing that server.
func (s *Store) ReorderServers(ctx context.Context, ids []string) error {
	return s.reorder(ctx, "servers", ids)
}

// ReorderForwards stores the console order of all forwards, with the same
// completeness rule as ReorderServers.
func (s *Store) ReorderForwards(ctx context.Context, ids []string) error {
	return s.reorder(ctx, "forwards", ids)
}

func (s *Store) reorder(ctx context.Context, table string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM "+table)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = false
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stale := fmt.Errorf("%w: 列表已变化，请刷新后重新排序", ErrConflict)
	if len(ids) != len(existing) {
		return stale
	}
	for position, id := range ids {
		if placed, ok := existing[id]; !ok || placed {
			return stale
		}
		existing[id] = true
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET sort_order=? WHERE id=?", position+1, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
