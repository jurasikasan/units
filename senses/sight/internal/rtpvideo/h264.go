package rtpvideo

import "bytes"

// NAL unit types (H.264 Table 7-1, RFC 6184 Table 1) the assembler cares about.
const (
	nalIDR   = 5
	nalSPS   = 7
	nalPPS   = 8
	nalSTAPA = 24
	nalFUA   = 28
)

var startCode = []byte{0, 0, 0, 1}

// AccessUnit is one picture's worth of H.264 (RFC 6184: the NAL units of one
// RTP timestamp) in Annex B form, ready for a decoder.
type AccessUnit struct {
	Data []byte
	// TS is the RTP timestamp, extended past its 32-bit wrap, at 90 kHz.
	TS int64
	// Key is an IDR picture with the SPS and PPS a decoder needs before it:
	// decoding can start here.
	Key bool
	// Broken is an access unit a packet of which was lost, or that used a
	// packetization sight does not take (STAP-B, MTAP, FU-B). Its data is
	// not to be decoded.
	Broken bool
}

// assembler builds access units from RTP payloads given in sequence order:
// single NAL unit packets, STAP-A and FU-A, that is packetization modes 0
// and 1 of RFC 6184.
type assembler struct {
	cur     bytes.Buffer
	ts      int64
	have    bool
	broken  bool
	idr     bool
	sps     bool
	pps     bool
	inFU    bool
	lastSPS []byte // the latest SPS and PPS seen, for an IDR that comes without them
	lastPPS []byte
}

// lost says a packet is missing before the next one: the unit being built,
// or the next one when none is, is broken. A whole lost picture breaks the
// one after it, so decoding waits for the next IDR whichever was lost.
func (a *assembler) lost() {
	a.broken = true
	a.inFU = false
}

// push takes the next payload in sequence order and returns the access
// units it finished: the previous one when the timestamp moved on, and
// this one when its marker bit says it is the last packet of it.
func (a *assembler) push(payload []byte, ts int64, marker bool) []AccessUnit {
	var done []AccessUnit
	if a.have && ts != a.ts {
		done = append(done, a.flush())
	}
	if !a.have {
		a.have, a.ts = true, ts
	}
	a.take(payload)
	if marker {
		done = append(done, a.flush())
	}
	return done
}

func (a *assembler) take(p []byte) {
	if len(p) < 1 {
		a.broken = true
		return
	}
	switch t := p[0] & 0x1f; {
	case t >= 1 && t <= 23:
		a.inFU = false
		a.nal(p)
	case t == nalSTAPA:
		a.inFU = false
		for rest := p[1:]; len(rest) > 0; {
			if len(rest) < 2 {
				a.broken = true
				return
			}
			n := int(rest[0])<<8 | int(rest[1])
			if n == 0 || len(rest) < 2+n {
				a.broken = true
				return
			}
			a.nal(rest[2 : 2+n])
			rest = rest[2+n:]
		}
	case t == nalFUA:
		if len(p) < 2 {
			a.broken = true
			return
		}
		start, end := p[1]&0x80 != 0, p[1]&0x40 != 0
		if start {
			hdr := p[0]&0xe0 | p[1]&0x1f
			a.cur.Write(startCode)
			a.cur.WriteByte(hdr)
			a.mark(hdr & 0x1f)
			a.inFU = true
		} else if !a.inFU {
			a.broken = true // the fragment's start was lost
			return
		}
		a.cur.Write(p[2:])
		if end {
			a.inFU = false
		}
	default: // STAP-B, MTAP16/24, FU-B: interleaved mode, not taken
		a.broken = true
	}
}

func (a *assembler) nal(n []byte) {
	t := n[0] & 0x1f
	a.mark(t)
	switch t {
	case nalSPS:
		a.lastSPS = append(a.lastSPS[:0], n...)
	case nalPPS:
		a.lastPPS = append(a.lastPPS[:0], n...)
	}
	a.cur.Write(startCode)
	a.cur.Write(n)
}

func (a *assembler) mark(t byte) {
	switch t {
	case nalIDR:
		a.idr = true
	case nalSPS:
		a.sps = true
	case nalPPS:
		a.pps = true
	}
}

func (a *assembler) flush() AccessUnit {
	au := AccessUnit{TS: a.ts, Broken: a.broken || a.inFU}
	data := a.cur.Bytes()
	if a.idr && !au.Broken {
		// An IDR whose parameter sets came earlier, in a unit of their own,
		// gets them again in front: the decoder may not have been fed those.
		var pre []byte
		if !a.sps && a.lastSPS != nil {
			pre = append(append(pre, startCode...), a.lastSPS...)
		}
		if !a.pps && a.lastPPS != nil {
			pre = append(append(pre, startCode...), a.lastPPS...)
		}
		data = append(pre, data...)
		au.Key = (a.sps || a.lastSPS != nil) && (a.pps || a.lastPPS != nil)
	}
	au.Data = append([]byte(nil), data...)
	a.cur.Reset()
	a.have, a.broken, a.idr, a.sps, a.pps, a.inFU = false, false, false, false, false, false
	return au
}
