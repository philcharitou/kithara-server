package main

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Chapter names from what the narrator says.
//
// Rips with no chapter marks of their own leave titles like "001" or "Chapter 1",
// which are wrong the moment a book opens with credits or a prologue. The narrator
// almost always reads the heading aloud, so the transcript's first words at each mark
// are a better source. This is the Android app's SpokenChapterTitle, ported, so the
// phone and the server name chapters the same way; it is deliberately conservative.

var (
	numberedHeadings   = set("chapter", "part", "book", "act", "scene", "stave", "canto", "volume", "episode", "session")
	standaloneHeadings = set("prologue", "epilogue", "interlude", "introduction", "preface", "foreword", "afterword", "appendix", "dedication", "intermission", "coda")
	proseBefore        = set("the", "a", "an", "this", "that", "his", "her", "their", "our", "my", "its", "of", "out", "into")
	numberFillers      = set("the", "number", "no")
	numberUnits        = map[string]int{
		"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
		"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
		"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8, "ninth": 9, "tenth": 10,
		"eleventh": 11, "twelfth": 12, "thirteenth": 13, "fourteenth": 14, "fifteenth": 15, "sixteenth": 16, "seventeenth": 17, "eighteenth": 18, "nineteenth": 19,
	}
	numberTens = map[string]int{
		"twenty": 20, "thirty": 30, "forty": 40, "fourty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
		"twentieth": 20, "thirtieth": 30, "fortieth": 40, "fiftieth": 50, "sixtieth": 60, "seventieth": 70, "eightieth": 80, "ninetieth": 90,
	}
	// Titles that carry nothing but a number: "Chapter 12", "Track 3", "012".
	genericTitle = regexp.MustCompile(`(?i)^(chapter|track|part|file)?\s*0*\d{1,4}\.?$`)
	quoteMarks   = regexp.MustCompile(`["“”()\[\]]`)
)

const (
	headingSearchWords = 14 // how far in a "chapter" heading may sit (an ident or the title can come first)
	headingNearStart   = 4  // for part, act, scene…
	standaloneWithin   = 8  // for prologue, epilogue…
	maxSubtitleWords   = 8
)

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// isGeneric reports whether a title says nothing a narrator could not say better.
func isGeneric(title string) bool { return genericTitle.MatchString(strings.TrimSpace(title)) }

// spokenHeading is a heading the narrator read out.
type spokenHeading struct {
	word     string
	number   int
	numbered bool
	subtitle string
}

func (h spokenHeading) isChapter() bool { return h.word == "Chapter" && h.numbered }

func (h spokenHeading) title() string {
	t := h.word
	if h.numbered {
		t += " " + strconv.Itoa(h.number)
	}
	if h.subtitle != "" {
		t += ": " + h.subtitle
	}
	return t
}

// parseHeading finds the heading at the start of what was heard, if there is one.
// "chapter" may follow a studio ident or the book's own name; other numbered headings
// only sit near the start, "book" and "volume" only first, and the standalone ones only
// early and not after "the" or "an" (then they are prose).
func parseHeading(heard string) *spokenHeading {
	words := strings.Fields(quoteMarks.ReplaceAllString(strings.ReplaceAll(heard, "’", "'"), " "))
	var found *spokenHeading
	for i := 0; i < min(len(words), headingSearchWords); i++ {
		word := bareWord(words[i])
		if standaloneHeadings[word] {
			before := ""
			if i > 0 {
				before = bareWord(words[i-1])
			}
			if i < standaloneWithin && !proseBefore[before] {
				return &spokenHeading{word: capitalise(word)}
			}
			continue
		}
		if !numberedHeadings[word] {
			continue
		}
		within := headingNearStart
		switch word {
		case "chapter":
			within = headingSearchWords
		case "book", "volume", "episode", "session":
			within = 1
		}
		if i >= within {
			continue
		}
		rest := words[i+1:]
		n, used, colon, ok := leadingNumber(rest)
		if !ok {
			continue
		}
		h := &spokenHeading{word: capitalise(word), number: n, numbered: true, subtitle: spokenSubtitle(rest[used:], colon)}
		if word == "chapter" {
			return h
		}
		found = h
	}
	return found
}

// leadingNumber reads a number at the start of words: digits, or words ("twenty-three",
// "twenty third"), after an optional filler ("Part the third", "Chapter number nine").
func leadingNumber(all []string) (value, used int, colon, ok bool) {
	if len(all) == 0 {
		return 0, 0, false, false
	}
	skipped := 0
	if numberFillers[bareWord(all[0])] {
		skipped = 1
	}
	words := all[skipped:]
	if len(words) == 0 {
		return 0, 0, false, false
	}
	if n, err := strconv.Atoi(bareWord(words[0])); err == nil && n >= 0 && n <= 999 {
		return n, skipped + 1, endsWithColon(words[0]), true
	}
	total, n, saw := 0, 0, false
	for n < len(words) && n < 3 {
		token := bareWord(words[n])
		parts := strings.FieldsFunc(token, func(r rune) bool { return r == '-' })
		value, good := 0, len(parts) > 0
		for _, p := range parts {
			if v, isTen := numberTens[p]; isTen {
				value += v
			} else if v, isUnit := numberUnits[p]; isUnit {
				value += v
			} else {
				good = false
			}
		}
		// "twenty three" as two words: the second must be a unit, not another ten.
		if _, isTen := numberTens[token]; !good || saw && (isTen || total%10 != 0) {
			break
		}
		total += value
		saw = true
		n++
		colon = endsWithColon(words[n-1])
		if colon || total%10 != 0 || value >= 100 {
			break
		}
	}
	return total, skipped + n, colon, saw
}

// spokenSubtitle is the words after the number when they are punctuated as a heading
// ("Chapter 23: The Fall"), not as the chapter's first sentence ("Chapter 23. Carl woke").
func spokenSubtitle(words []string, afterColon bool) string {
	if len(words) == 0 {
		return ""
	}
	introduced := afterColon
	if !introduced && slices.Contains([]string{":", "-", "–", "—"}, words[0]) {
		introduced = true
		words = words[1:]
	}
	if !introduced {
		return ""
	}
	var taken []string
	for _, w := range words[:min(len(words), maxSubtitleWords)] {
		taken = append(taken, strings.TrimRight(w, ".,;!?:"))
		if strings.ContainsAny(w, ".!?;") {
			break
		}
	}
	t := strings.TrimSpace(strings.Join(taken, " "))
	if len([]rune(t)) > 60 {
		return ""
	}
	return t
}

// resolveChapterNames gives titles for a whole book from what was heard at each mark, in
// order ("" keeps the existing title). A heard chapter number is trusted when another
// agrees with it or it sits in order between its neighbours; a gap between two trusted
// numbers is filled when the count of marks in it is exactly right; before the first
// and after the last, generic titles say what the mark is rather than inventing numbers.
func resolveChapterNames(heard, existing []string) []string {
	headings := make([]*spokenHeading, len(heard))
	var anchors []int
	for i, h := range heard {
		headings[i] = parseHeading(h)
		if headings[i] != nil && headings[i].isChapter() {
			anchors = append(anchors, i)
		}
	}
	n := func(i int) int { return headings[i].number }
	// Distance in chapter-bearing marks: an interlude between two chapters does not
	// make them one further apart.
	slot := make([]int, len(heard))
	count := 0
	for i, h := range headings {
		slot[i] = count
		if h == nil || h.isChapter() {
			count++
		}
	}
	trusted := map[int]bool{}
	for k, a := range anchors {
		agreed := slices.ContainsFunc(anchors, func(b int) bool { return b != a && n(b)-n(a) == slot[b]-slot[a] })
		inOrder := (k == 0 || n(anchors[k-1]) < n(a)) && (k == len(anchors)-1 || n(a) < n(anchors[k+1]))
		if agreed || inOrder {
			trusted[a] = true
		}
	}
	result := make([]string, len(heard))
	for i, h := range headings {
		if h != nil && (!h.isChapter() || trusted[i]) {
			result[i] = h.title()
		}
	}
	var ordered []int
	for i := range heard {
		if trusted[i] {
			ordered = append(ordered, i)
		}
	}
	for k := 0; k+1 < len(ordered); k++ {
		a, b := ordered[k], ordered[k+1]
		var open []int
		for i := a + 1; i < b; i++ {
			if headings[i] == nil || headings[i].isChapter() && !trusted[i] {
				open = append(open, i)
			}
		}
		if n(b)-n(a)-1 != len(open) {
			continue
		}
		for off, i := range open {
			result[i] = "Chapter " + strconv.Itoa(n(a)+off+1)
		}
	}
	if len(existing) == len(heard) && len(ordered) > 0 {
		first, last := ordered[0], ordered[len(ordered)-1]
		for i := range heard {
			if result[i] != "" || !isGeneric(existing[i]) {
				continue
			}
			switch {
			case i < first && i == 0:
				result[i] = "Opening credits"
			case i < first:
				result[i] = "Before chapter " + strconv.Itoa(n(first))
			case i > last && i == len(heard)-1:
				result[i] = "Closing credits"
			case i > last:
				result[i] = "After chapter " + strconv.Itoa(n(last))
			}
		}
	}
	return result
}

func bareWord(w string) string { return strings.ToLower(strings.Trim(w, ".,:;!?'-")) }

func endsWithColon(w string) bool { return strings.HasSuffix(strings.TrimRight(w, ".,;"), ":") }

func capitalise(w string) string {
	r := []rune(w)
	if len(r) == 0 {
		return w
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ------------------------------------------------------------------ route

// ChapterName is a suggestion for one chapter.
type ChapterName struct {
	Index     int    `json:"index"`
	Current   string `json:"current"`
	Suggested string `json:"suggested"`
	Generic   bool   `json:"generic"` // the current title is only a number
}

// headingWindow is how much of each chapter's start is read for its heading.
const headingWindow = 25000

// chapterNames (admin) suggests chapter names from a book's transcript: only where the
// suggestion differs from the title there now.
func (s *server) chapterNames(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	t, err := s.loadTranscript(b)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A book without chapters has none to name (its one "chapter" is the whole book).
	chs := b.Chapters
	heard := make([]string, len(chs))
	existing := make([]string, len(chs))
	for i, c := range chs {
		existing[i] = c.Title
		var words []string
		for _, p := range t.Passages {
			if p.StartMs >= c.StartMs-1000 && p.StartMs < min(c.StartMs+headingWindow, c.EndMs) {
				words = append(words, p.Text)
			}
		}
		heard[i] = strings.Join(words, " ")
	}
	out := []ChapterName{}
	for i, name := range resolveChapterNames(heard, existing) {
		if name != "" && name != existing[i] {
			out = append(out, ChapterName{i, existing[i], name, isGeneric(existing[i])})
		}
	}
	writeJSONResponse(w, 200, map[string]any{"suggestions": out, "transcribed": len(t.Passages) > 0})
}
