package buffer

// RetainedScrapesForTest reports how many *Scrape pointers the backing array
// still holds, INCLUDING the slots outside the live window.
//
// The distinction is the whole point. Depth() counts what the ring says it is
// holding; this counts what the garbage collector can still reach through it.
// Add and AckThrough both move the window without touching the slots they
// leave behind, so before those slots were zeroed the two numbers diverged --
// a ring reporting Depth() == 0 could still be pinning every scrape it had
// ever buffered, each one a host row plus every per-entity row measured with
// it.
//
// Reaching for the backing array directly rather than a finalizer or a weak
// pointer because this must fail deterministically. A GC-observation test
// passes or fails on the collector's mood, which is exactly the kind of test
// that gets marked flaky and then deleted.
func (r *Ring) RetainedScrapesForTest() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	whole := r.entries[:cap(r.entries)]
	n := 0
	for i := range whole {
		if whole[i].Scrape != nil {
			n++
		}
	}
	return n
}
