package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// What a reading tool shows stays under the output limit (MaxToolOutput),
// which would otherwise cut its middle out: it ends at a line (or a page)
// boundary and says exactly where to read on, so nothing is skipped.

// readShowRunes is what a reading tool shows at most: room is left for its notes.
const readShowRunes = MaxToolOutput - 1000

// maxLineRunes is the longest line shown whole (a minified file has one huge line).
const maxLineRunes = 3000

// fitText cuts text to readShowRunes at the last line that fits whole. It
// returns what is shown and whether anything was cut.
func fitText(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	cut := textutil.TruncateRunes(text, limit, "")
	if i := strings.LastIndexByte(cut, '\n'); i > len(cut)/2 {
		cut = cut[:i+1]
	}
	return cut, true
}

var (
	pageHeadRe  = regexp.MustCompile(`(?m)^--- Page (\d+)/(\d+) ---$`)
	pageRangeRe = regexp.MustCompile(`^\s*(\d+)\s*(?:-\s*(\d*))?`)
)

// pageRange reads the first range of a pages argument ("5-9", "12-", "3").
func pageRange(s string) (from, to int) {
	m := pageRangeRe.FindStringSubmatch(s)
	if m == nil {
		return 1, 0
	}
	from, _ = strconv.Atoi(m[1])
	switch {
	case !strings.Contains(m[0], "-"):
		to = from
	case m[2] != "":
		to, _ = strconv.Atoi(m[2])
	}
	return max(from, 1), to
}

// fitPages keeps the whole pages of a PDF's text that fit, and says which page to read on from.
func fitPages(text string) string {
	if utf8.RuneCountInString(text) <= readShowRunes {
		return text
	}
	cut, _ := fitText(text, readShowRunes)
	heads := pageHeadRe.FindAllStringSubmatchIndex(cut, -1)
	if len(heads) >= 2 {
		last := heads[len(heads)-1] // the page cut off starts here: it is read next
		next, _ := strconv.Atoi(cut[last[2]:last[3]])
		return strings.TrimRight(cut[:last[0]], "\n") + fmt.Sprintf("\n... [truncated to fit: read on with pages=\"%d-\", or ask document_reader]", next)
	}
	if len(heads) == 1 { // one long page: its start is shown
		page, _ := strconv.Atoi(cut[heads[0][2]:heads[0][3]])
		return strings.TrimRight(cut, "\n") + fmt.Sprintf("\n... [page %d is longer than one read: the rest of it is not shown; "+
			"ask document_reader about it, or read on with pages=\"%d-\"]", page, page+1)
	}
	return strings.TrimRight(cut, "\n") + "\n... [truncated]"
}
