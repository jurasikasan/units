// Package device is phone as its contract describes it: dial, answer, listen
// and hangup over calls (phone/CONTRACT.md §2). Audio rides the answered
// call's audio_in socket, opened by the call layer; the device only places,
// takes, listens and ends calls.
package device

import (
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/call"
)

type Device struct {
	Calls *call.Manager
}

func New(calls *call.Manager) *Device {
	return &Device{Calls: calls}
}

func (d *Device) Dial(req call.DialRequest) (string, error) { return d.Calls.Dial(req) }

// Answer takes a ringing inbound call: phone replies 200 OK with its SDP
// (revision 4, §2.6).
func (d *Device) Answer(callID string) error { return d.Calls.Answer(callID) }

// Listen moves the call's far-end audio to to (host:port, UDP); to:"" stops
// it (revision 3, §2.3).
func (d *Device) Listen(callID, to string) error { return d.Calls.Listen(callID, to) }

func (d *Device) Hangup(callID string) { d.Calls.Hangup(callID) }
