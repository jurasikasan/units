package audioin

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBindsPortInRange(t *testing.T) {
	l, err := New(43000, 43199)
	require.NoError(t, err)
	defer l.Close() //nolint:errcheck
	p := l.Port()
	require.GreaterOrEqual(t, p, 43000)
	require.LessOrEqual(t, p, 43199)

	// The port is taken: the next listener takes the next one.
	l2, err := New(43000, 43199)
	require.NoError(t, err)
	defer l2.Close() //nolint:errcheck
	require.NotEqual(t, p, l2.Port())
}

func TestBindsEphemeralWhenRangeIsEmpty(t *testing.T) {
	l, err := New(0, 0)
	require.NoError(t, err)
	defer l.Close() //nolint:errcheck
	require.Greater(t, l.Port(), 0)
}

func TestRangeWithoutFreePortFails(t *testing.T) {
	taken, err := New(43077, 43077)
	require.NoError(t, err)
	defer taken.Close() //nolint:errcheck
	_, err = New(43077, 43077)
	require.Error(t, err, "no free port in the range")
}

func TestReadDeliversPacketsAndCloseUnblocks(t *testing.T) {
	l, err := New(0, 0)
	require.NoError(t, err)
	dst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: l.Port()}

	var got atomic.Int32
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		l.Read(ctx, func(pt uint8, _ uint32, payload []byte) {
			require.EqualValues(t, 96, pt)
			require.Equal(t, uint16(1), binary.BigEndian.Uint16(payload[:2]))
			got.Add(1)
		})
		close(done)
	}()

	conn, err := net.DialUDP("udp", nil, dst)
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	// 12-byte RTP header: PT 96, SSRC 0xA11CE, then a 16-bit big-endian payload 1.
	buf := make([]byte, 12)
	buf[1] = 96
	binary.BigEndian.PutUint32(buf[8:], 0xA11CE)
	_, err = conn.Write(append(buf, 0x00, 0x01))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return got.Load() == 1 }, time.Second, 10*time.Millisecond)

	cancel()
	l.Close() //nolint:errcheck
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Read did not return after Close")
	}
}

func TestL16ToSamplesIsBigEndian(t *testing.T) {
	in := []byte{0x01, 0x02, 0x80, 0x00}
	out := L16ToSamples(in)
	require.Equal(t, []int16{0x0102, -0x8000}, out)
	require.Len(t, L16ToSamples(nil), 0)
}
