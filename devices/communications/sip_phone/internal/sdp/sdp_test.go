package sdp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/rtp"
)

func TestBuildOffer_CRLFAndBothG711(t *testing.T) {
	offer := string(BuildOffer("203.0.113.5", 42000))

	assert.NotContains(t, strings.ReplaceAll(offer, "\r\n", ""), "\n", "every line ends in CRLF")
	assert.Contains(t, offer, "c=IN IP4 203.0.113.5\r\n")
	assert.Contains(t, offer, "m=audio 42000 RTP/AVP 8 0\r\n")
	assert.Contains(t, offer, "a=rtpmap:8 PCMA/8000\r\n")
	assert.Contains(t, offer, "a=rtpmap:0 PCMU/8000\r\n")
}

// The far end's choice wins: an answer listing PCMU first gets PCMU even though
// phone prefers PCMA.
func TestParseAnswer_TakesTheAnswersCodec(t *testing.T) {
	a, err := ParseAnswer("v=0\r\nc=IN IP4 198.51.100.7\r\nm=audio 30000 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\n")
	require.NoError(t, err)
	assert.Equal(t, rtp.PayloadTypePCMU, a.Codec)
	assert.Equal(t, "198.51.100.7:30000", a.Addr.String())
}

func TestParseAnswer_NoOfferedCodecIsAnError(t *testing.T) {
	_, err := ParseAnswer("v=0\r\nc=IN IP4 198.51.100.7\r\nm=audio 30000 RTP/AVP 18\r\n")
	assert.Error(t, err)
}

func TestParseAnswer_HostnameInC(t *testing.T) {
	a, err := ParseAnswer("c=IN IP4 sbc.example.net\nm=audio 30000 RTP/AVP 8\n")
	require.NoError(t, err)
	assert.True(t, a.Addr.IP.IsUnspecified())
	assert.Equal(t, rtp.PayloadTypePCMA, a.Codec)
}
