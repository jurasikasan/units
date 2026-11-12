package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The envelope is shared/events' and tested there; what is this unit's is the
// stream and the source the publisher writes with.
func TestRedis_StreamAndSource(t *testing.T) {
	r := Redis(nil)
	assert.Equal(t, Stream, r.Stream)
	assert.Equal(t, Source, r.Source)
}
