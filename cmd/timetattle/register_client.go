package main

import (
	"context"
	"os"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// recordClientAssertion appends this registration's client to the append-only
// client-project log, unless the log already says exactly that.
//
// effectiveFrom is the zero time for a first registration or a correction —
// both of which reach back over evidence that already exists — and the date of
// the change for a reassignment. `timetattle register` refuses to guess between
// the last two; see the -since and -correction flags.
//
// # Why it appends nothing when nothing changed
//
// 0012 deliberately carries no UNIQUE index, following 0008: recording the same
// assertion twice is a fact, and the fold is idempotent under repetition. But a
// script that registers unconditionally on every run would then grow the log
// without ever changing an answer, so the no-op is enforced HERE, at the caller,
// which is also where registry.Register already treats an unchanged marker as a
// no-op. The two behave the same way for the same reason.
//
// # Why a failure here is not a failure of the registration
//
// The marker is the fact; the log is a derived record of it that
// backfillClientAssertions will fill in on the next harvest if this could not.
// So a missing store, or one another process holds, costs the user nothing
// permanent — exactly the tolerance the registry index already gets — and the
// caller reports it rather than failing a registration that succeeded.
func recordClientAssertion(storePath string, m *registry.Marker, effectiveFrom time.Time) error {
	var err error
	explicit := storePath != ""
	if storePath == "" {
		if storePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	// REGISTERING MUST NOT CREATE A STORE, and this is not tidiness. store.Open
	// creates and migrates whatever path it is handed, so a bare `timetattle
	// register` would build an empty event store as a side effect — on the real
	// machine, from any test that registers a directory, which is precisely the
	// pollution registry.RecordInIndex's doc explains at length and which
	// TestSessionizeWritesOnlyToTheStoreItWasGiven exists to catch. It caught
	// this.
	//
	// A store that is not there yet is not an error either. The marker is the
	// fact; backfillClientAssertions records it on the first harvest, which is
	// the same run that would have created the store anyway.
	if !explicit {
		if _, err := os.Stat(storePath); err != nil {
			return nil
		}
	}
	st, err := store.Open(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	known, err := st.ClientMap(ctx, time.Time{})
	if err != nil {
		return err
	}
	// "What does this directory already say" rather than "what does the project
	// resolve to". A project several directories share can resolve correctly
	// while THIS directory has never been recorded, and skipping on the weaker
	// question would leave it unrecorded for ever — and its disagreement, if it
	// disagreed, invisible.
	if current, ok := known.AssertedByAt(m.Project, m.Path, effectiveFrom); ok && current == m.Client {
		return nil
	}
	return st.AppendClientAssertion(ctx, clientmap.Assertion{
		Project: m.Project, Client: m.Client, Source: m.Path,
		EffectiveFrom: effectiveFrom, RecordedAt: time.Now().UTC(),
	}, "register")
}
