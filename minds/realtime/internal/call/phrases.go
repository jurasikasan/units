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
}

// heard is what of an answer was heard, given that its sound was played up
// to ms. speaking reports what went out per phrase and when it ended, not
// where each word lay, so within a phrase the place is a guess by the share
// of letters, cut at a word, a word more than half heard counting
// (UNIT-51 would make it exact).
func heard(pieces []*piece, ms int) string {
	var out []string
	for _, p := range pieces {
		said := p.spoken
		if p.cause == "normal" || p.cause == "" {
			said = p.text
		}
		if said == "" {
			break
		}
		if ms >= p.endMs {
			out = append(out, said)
			continue
		}
		if ms > p.startMs && p.endMs > p.startMs {
			if part := share(said, float64(ms-p.startMs)/float64(p.endMs-p.startMs)); part != "" {
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
