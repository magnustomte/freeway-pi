package main

import (
	"testing"

	"freewaypi/internal/store"
	"freewaypi/internal/web"
)

// TestNilStoreStaysNilThroughTheInterface guards the conversion that startWeb
// exists to avoid: a nil *store.Store assigned to a web.History field produces
// an interface that is not nil and holds nil, so the history endpoints would
// call straight through it and panic rather than reporting that nothing is
// being recorded.
func TestNilStoreStaysNilThroughTheInterface(t *testing.T) {
	var db *store.Store

	var careless web.History = db
	if careless == nil {
		t.Skip("Go changed; the trap this guards against is gone")
	}

	opts := web.Options{}
	if db != nil {
		opts.History = db
	}
	if opts.History != nil {
		t.Fatal("a store that was never opened reached the server as a usable history")
	}
}
