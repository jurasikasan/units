package call

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// phrases cuts a model's words, as they stream, into what speaking is given
// one `speak` at a time: a sentence each, so the first is said while the
// model still writes the rest (speaking synthesises one piece ahead).
type phrases struct{ buf strings.Builder }

// feed adds words and returns the sentences they finished.
func (p *phrases) feed(s string) []string {
	p.buf.WriteString(s)
	text := p.buf.String()
	var out []string
	start := 0
	for i, r := range text {
		if !strings.ContainsRune(".!?…\n", r) {
			continue
		}
		next := i + utf8.RuneLen(r)
		if next >= len(text) {
			break // the end of what came: the next piece may go on (3.14, "?!")
		}
		after, _ := utf8.DecodeRuneInString(text[next:])
		if !unicode.IsSpace(after) {
			continue
		}
		if sentence := strings.TrimSpace(text[start:next]); sentence != "" {
			out = append(out, sentence)
		}
		start = next
	}
	p.buf.Reset()
	p.buf.WriteString(text[start:])
	return out
}

// flush returns what is left when the model has finished.
func (p *phrases) flush() []string {
	rest := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	if rest == "" {
		return nil
	}
	return []string{rest}
}

// piece is one phrase given to speaking, and where its sound lay in the
// answer's sound as it went out (ms from the answer's first sound).
type piece struct {
	request string
	text    string
	spoken  string // speaking's word: what went out
	cause   string // speaking's completion cause; "" while it plays
	startMs int
	endMs   int
	// speaking's marker: the timestamp its sound began at in speaking's
	// stream, and whether that place in the answer's sound is known (placed:
	// startMs is exact, not the previous phrase's end as speak-complete told).
	marked, placed bool
	markTs         uint32
}

// heard is what of an answer was heard, given that its sound was played up
// to ms. A phrase's start is exact where speaking marked it (its marker's
// timestamp found in the sound, UNIT-51), and a phrase ends where the next
// one placed begins; else both come from speak-complete, as it arrived.
// Within a phrase the place is a guess by the share of letters, cut at a
// word, a word more than half heard counting: no word timings (no karaoke).
func heard(pieces []*piece, ms int) string {
	var out []string
	for i, p := range pieces {
		end := p.endMs
		if i+1 < len(pieces) && pieces[i+1].placed && p.cause != "" {
			end = pieces[i+1].startMs
		}
		said := p.spoken
		if p.cause == "normal" || p.cause == "" {
			said = p.text
		}
		if said == "" {
			break
		}
		if ms >= end {
			out = append(out, said)
			continue
		}
		if ms > p.startMs && end > p.startMs {
			if part := share(said, float64(ms-p.startMs)/float64(end-p.startMs)); part != "" {
				out = append(out, part)
			}
		}
		break
	}
	return strings.Join(out, " ")
}

// share is the words of s that lie within the first frac of its letters,
// a word counting when more than half of it does.
func share(s string, frac float64) string {
	runes := []rune(s)
	limit := frac * float64(len(runes))
	end := 0
	for i := 0; i < len(runes); {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		j := i
		for j < len(runes) && !unicode.IsSpace(runes[j]) {
			j++
		}
		if j == i {
			break
		}
		if float64(i)+float64(j-i)/2 >= limit {
			break
		}
		end = j
		i = j
	}
	return string(runes[:end])
}
