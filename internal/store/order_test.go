package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestReorderServersKeepsTheConsoleOrderAndAppendsNewServers(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	c, _, err := s.CreateServer(ctx, model.Server{Name: "Third", Address: "198.51.100.3"})
	if err != nil {
		t.Fatal(err)
	}
	serverIDs := func() []string {
		t.Helper()
		servers, err := s.ListServers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, server := range servers {
			ids = append(ids, server.ID)
		}
		return ids
	}
	if got := serverIDs(); !slices.Equal(got, []string{a.ID, b.ID, c.ID}) {
		t.Fatalf("servers are not listed in creation order: %v", got)
	}
	if err := s.ReorderServers(ctx, []string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if got := serverIDs(); !slices.Equal(got, []string{c.ID, a.ID, b.ID}) {
		t.Fatalf("servers do not follow the stored order: %v", got)
	}
	d, _, err := s.CreateServer(ctx, model.Server{Name: "Fourth", Address: "198.51.100.4"})
	if err != nil {
		t.Fatal(err)
	}
	if got := serverIDs(); !slices.Equal(got, []string{c.ID, a.ID, b.ID, d.ID}) {
		t.Fatalf("a new server is not placed last: %v", got)
	}
	for _, ids := range [][]string{
		{c.ID, a.ID, b.ID},
		{c.ID, a.ID, b.ID, b.ID},
		{c.ID, a.ID, b.ID, "srv_missing"},
		{c.ID, a.ID, b.ID, d.ID, "srv_missing"},
	} {
		if err := s.ReorderServers(ctx, ids); !errors.Is(err, ErrConflict) {
			t.Fatalf("incomplete order %v was not rejected: %v", ids, err)
		}
	}
	if got := serverIDs(); !slices.Equal(got, []string{c.ID, a.ID, b.ID, d.ID}) {
		t.Fatalf("a rejected order changed the stored one: %v", got)
	}
}

func TestReorderForwardsKeepsTheConsoleOrder(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	var ids []string
	for index, ingress := range []string{a.ID, b.ID, a.ID} {
		forward, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: ingress, ListenPort: uint16(25001 + index),
			TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, forward.ID)
	}
	forwardIDs := func(list func(context.Context) ([]model.Forward, error)) []string {
		t.Helper()
		forwards, err := list(ctx)
		if err != nil {
			t.Fatal(err)
		}
		result := []string{}
		for _, forward := range forwards {
			result = append(result, forward.ID)
		}
		return result
	}
	if got := forwardIDs(s.ListForwards); !slices.Equal(got, ids) {
		t.Fatalf("forwards are not listed in creation order: %v", got)
	}
	if err := s.ReorderForwards(ctx, []string{ids[2], ids[1], ids[0]}); err != nil {
		t.Fatal(err)
	}
	if got := forwardIDs(s.ListForwards); !slices.Equal(got, []string{ids[2], ids[1], ids[0]}) {
		t.Fatalf("forwards do not follow the stored order: %v", got)
	}
	onServer := func(ctx context.Context) ([]model.Forward, error) { return s.ListForwardsForServer(ctx, a.ID) }
	if got := forwardIDs(onServer); !slices.Equal(got, []string{ids[2], ids[0]}) {
		t.Fatalf("forwards of one server do not follow the stored order: %v", got)
	}
	if err := s.ReorderForwards(ctx, ids[:2]); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete order was not rejected: %v", err)
	}
}
