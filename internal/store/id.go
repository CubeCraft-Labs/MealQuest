package store

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	idMu      sync.Mutex
	idEntropy = ulid.Monotonic(rand.Reader, 0)
	idLastMS  uint64
)

// NewID returns a new ULID as its canonical 26-character string. IDs from
// this process sort strictly in creation order, including within one
// millisecond and across a backwards wall-clock step. It panics if the
// monotonic entropy overflows within a millisecond.
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()
	ms := max(ulid.Timestamp(time.Now()), idLastMS)
	idLastMS = ms
	return ulid.MustNew(ms, idEntropy).String()
}
