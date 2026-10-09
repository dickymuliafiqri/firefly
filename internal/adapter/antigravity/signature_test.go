package antigravity

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignatureStore_RecordAndLookup(t *testing.T) {
	t.Parallel()

	store := NewSignatureStore()
	assert.Equal(t, 0, store.Len())

	store.Record("call_abc", "sig-xyz")
	assert.Equal(t, 1, store.Len())
	assert.Equal(t, "sig-xyz", store.Lookup("call_abc"))

	// Unknown and empty ids miss.
	assert.Equal(t, "", store.Lookup("call_missing"))
	assert.Equal(t, "", store.Lookup(""))

	// Empty ids / signatures are ignored.
	store.Record("", "sig")
	store.Record("call_abc", "")
	assert.Equal(t, 1, store.Len())
	assert.Equal(t, "sig-xyz", store.Lookup("call_abc"))

	// Re-recording overwrites.
	store.Record("call_abc", "sig-2")
	assert.Equal(t, "sig-2", store.Lookup("call_abc"))
}

func TestSignatureStore_Expiry(t *testing.T) {
	t.Parallel()

	store := NewSignatureStore()
	store.ttl = 10 * time.Millisecond
	store.Record("call_1", "sig-1")

	assert.Equal(t, "sig-1", store.Lookup("call_1"))

	time.Sleep(25 * time.Millisecond)
	// Expired entry is dropped on lookup.
	assert.Equal(t, "", store.Lookup("call_1"))
	assert.Equal(t, 0, store.Len())
}

func TestSignatureStore_BoundedEviction(t *testing.T) {
	t.Parallel()

	store := NewSignatureStore()
	store.max = 100
	store.ttl = time.Hour

	for i := 0; i < 200; i++ {
		store.Record("call-"+strconv.Itoa(i), "sig")
	}

	// Never grows past the cap.
	assert.LessOrEqual(t, store.Len(), 100)

	// Newest entries survive, oldest quarter is evicted.
	store.Record("call-newest", "sig-new")
	assert.Equal(t, "sig-new", store.Lookup("call-newest"))
}

func TestSignatureStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := NewSignatureStore()
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := strconv.Itoa(w*1000 + i)
				store.Record(id, "sig-"+id)
				store.Lookup(id)
			}
		}(w)
	}
	wg.Wait()
	require.Positive(t, store.Len())
}
