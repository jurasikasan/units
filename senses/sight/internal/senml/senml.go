// Package senml reads a SenML pack (RFC 8428, JSON) into resolved records:
// base fields applied, the name whole, the time absolute. It keeps what sight
// takes in — string values (vs) and data values (vd) — and nothing it does not.
package senml

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// ContentType is SenML's JSON media type (RFC 8428 §12.3).
const ContentType = "application/senml+json"

// relativeBelow is the boundary of RFC 8428 §4.5.3: a time below 2**28 is
// relative to now, one at or above it is seconds since the Unix epoch.
const relativeBelow = 1 << 28

// Record is one resolved record: the name with the base name before it, the
// time absolute, and the value it carries.
type Record struct {
	Name string
	Time time.Time
	// String is vs; HasString says it was there (an empty string is a value).
	String    string
	HasString bool
	// Data is vd decoded from base64url; nil when the record had none.
	Data []byte
}

type raw struct {
	BaseName    *string  `json:"bn"`
	BaseTime    *float64 `json:"bt"`
	BaseVersion *int     `json:"bver"`
	Name        *string  `json:"n"`
	Time        *float64 `json:"t"`
	StringValue *string  `json:"vs"`
	DataValue   *string  `json:"vd"`
}

// Parse resolves a pack. now is the receiver's clock, the reference for
// relative times (RFC 8428 §4.5.3): it is when the pack arrived, read once,
// not when its records are processed. A field the reader must understand
// (its name ends in "_", §4.4) and does not is an error, as are a record
// with no name and a vd that is not base64url.
func Parse(body []byte, now time.Time) ([]Record, error) {
	var pack []map[string]json.RawMessage
	if err := json.Unmarshal(body, &pack); err != nil {
		return nil, fmt.Errorf("not a SenML pack: %w", err)
	}
	var (
		baseName string
		baseTime float64
		out      []Record
	)
	for i, m := range pack {
		for k := range m {
			if strings.HasSuffix(k, "_") {
				return nil, fmt.Errorf("record %d: field %q must be understood and is not", i, k)
			}
		}
		b, _ := json.Marshal(m)
		var r raw
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		if r.BaseVersion != nil && *r.BaseVersion > 10 {
			return nil, fmt.Errorf("record %d: bver %d is newer than this reader", i, *r.BaseVersion)
		}
		if r.BaseName != nil {
			baseName = *r.BaseName
		}
		if r.BaseTime != nil {
			baseTime = *r.BaseTime
		}
		rec := Record{Name: baseName}
		if r.Name != nil {
			rec.Name += *r.Name
		}
		if rec.Name == "" {
			return nil, fmt.Errorf("record %d: no name", i)
		}
		t := baseTime
		if r.Time != nil {
			t += *r.Time
		}
		rec.Time = resolve(t, now)
		if r.StringValue != nil {
			rec.String, rec.HasString = *r.StringValue, true
		}
		if r.DataValue != nil {
			d, err := decodeData(*r.DataValue)
			if err != nil {
				return nil, fmt.Errorf("record %d: vd: %w", i, err)
			}
			rec.Data = d
		}
		out = append(out, rec)
	}
	return out, nil
}

// resolve turns a SenML time into an absolute one, to the millisecond.
func resolve(t float64, now time.Time) time.Time {
	if math.Abs(t) >= relativeBelow {
		sec, frac := math.Modf(t)
		return time.Unix(int64(sec), int64(math.Round(frac*1e3))*int64(time.Millisecond)).UTC()
	}
	return now.Add(time.Duration(math.Round(t*1e3)) * time.Millisecond).UTC().Truncate(time.Millisecond)
}

// decodeData reads vd: base64url without padding (RFC 8428 §4.3); a padded
// one is taken too.
func decodeData(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// EncodeData is vd for b, as a sender writes it.
func EncodeData(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Kind is the last segment of a record's name, after the base name's
// separators (":", "/", "."): "urn:dev:cam1:frame" is "frame".
func (r Record) Kind() string {
	i := strings.LastIndexAny(r.Name, ":/.")
	return r.Name[i+1:]
}
