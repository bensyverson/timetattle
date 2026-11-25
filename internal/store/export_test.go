package store

// EventsQuery is the SQL an event read issues, exposed to this package's
// external tests.
//
// It is here rather than made part of the API because nothing outside the store
// has any business composing a read, and the test that needs it is asking a
// question no caller can ask: does SQLite satisfy this query's ORDER BY from an
// index, or does it sort? Answering that against a COPY of the query would
// answer it about the copy.
var EventsQuery = eventsQuery

// UsageQuery is the SQL a usage roll-up issues, exposed for the same reason and
// asked the same question: which rows does SQLite have to touch to answer it.
var UsageQuery = usageQuery

// LocatorsUnderQuery is the listing a path-limited attribution issues for one
// directory.
const LocatorsUnderQuery = locatorsUnderQuery

// MatchIdentityQuery and FillPayloadQuery are the two statements a payload
// backfill issues, exposed for the same reason EventsQuery is: the question the
// plan test asks is about the TEXT, and asking it of a copy would answer it
// about the copy.
var (
	MatchIdentityQuery = matchIdentityQuery
	FillPayloadQuery   = fillPayloadQuery
)
