package mouth

import (
	"strings"
	"unicode"
)

// sentence is a span of the say's text, in runes.
type sentence struct {
	text       string
	start, end int
}

// splitSentences cuts after . ! ? … when whitespace follows, keeping each
// sentence's place in the original text so spoken can be cut from it.
func splitSentences(text string) []sentence {
	r := []rune(text)
	var out []sentence
	start := 0
	flush := func(end int) {
		s := strings.TrimSpace(string(r[start:end]))
		if s != "" {
			// start of the trimmed text within r
			lead := 0
			for start+lead < end && unicode.IsSpace(r[start+lead]) {
				lead++
			}
			out = append(out, sentence{text: s, start: start + lead, end: end})
		}
		start = end
	}
	for i, c := range r {
		if strings.ContainsRune(".!?…", c) && (i+1 == len(r) || unicode.IsSpace(r[i+1])) {
			flush(i + 1)
		}
	}
	flush(len(r))
	return out
}

// credited tracks how far into the text the audio went.
type credited struct {
	text string
	upTo int // runes of text fully played
}

// partial credits a sentence cut after f of its total frames, in proportion
// to its runes.
func (c *credited) partial(sn sentence, f, total int) {
	if total == 0 {
		return
	}
	c.upTo = sn.start + (sn.end-sn.start)*f/total
}

// spoken is the text up to upTo, cut at a word boundary: a word more than half
// played counts as spoken, a word less than that does not.
func (c *credited) spoken() string {
	r := []rune(c.text)
	pos := c.upTo
	if pos >= len(r) {
		return c.text
	}
	if pos <= 0 {
		return ""
	}
	ws := pos
	for ws > 0 && !unicode.IsSpace(r[ws-1]) {
		ws--
	}
	we := pos
	for we < len(r) && !unicode.IsSpace(r[we]) {
		we++
	}
	if ws < pos && (pos-ws)*2 > we-ws {
		pos = we
	} else {
		pos = ws
	}
	return strings.TrimRightFunc(string(r[:pos]), unicode.IsSpace)
}
