package main

import (
	"encoding/json"
	"os"
	"sync"
)

// reviewedSet remembers which finished background tasks the user has checked off, so the Tasks screen files them
// under Resolved on every device. It is a short list of ids in one small file, newest last, kept to the
// last few hundred: the tasks themselves are only listed for a while anyway.
type reviewedSet struct {
	path string
	mu   sync.Mutex
	ids  []string
}

const maxReviewed = 300

func newReviewedSet(path string) *reviewedSet {
	r := &reviewedSet{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &r.ids)
	}
	return r
}

func (r *reviewedSet) Has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.ids {
		if x == id {
			return true
		}
	}
	return false
}

// Add checks one task off. Checking the same one twice changes nothing.
func (r *reviewedSet) Add(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.ids {
		if x == id {
			return nil
		}
	}
	r.ids = append(r.ids, id)
	if len(r.ids) > maxReviewed {
		r.ids = r.ids[len(r.ids)-maxReviewed:]
	}
	raw, _ := json.Marshal(r.ids)
	return os.WriteFile(r.path, raw, 0o600)
}
