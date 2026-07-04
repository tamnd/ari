package colony

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// newID returns a time-sortable id: a microsecond timestamp big-endian, then
// random entropy, hex-encoded. It sorts by creation like a ULID, so board
// rows and handoffs order by age without pulling a ULID dependency in. It
// mirrors the memory store's id scheme so the two tables read alike.
func newID(now time.Time) string {
	var b [12]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMicro()))
	_, _ = rand.Read(b[8:])
	return hex.EncodeToString(b[:])
}
