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
