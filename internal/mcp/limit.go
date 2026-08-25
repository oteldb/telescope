package mcp

import (
	"strconv"

	"github.com/go-faster/errors"
)

// capLimit resolves how many a tool was asked for into how many it will return,
// and says so when the two differ.
//
// A limit above what a call answers with is lowered rather than refused:
// somebody asking for a thousand wants as many as they can have, and a refusal
// costs a round trip to learn a number the schema already wrote down. But the
// lowering is reported, for the same reason every other cut here is. The
// answer's own "this is as many as was asked for" would otherwise be quoting a
// number nobody asked for, and a caller comparing what it asked against what it
// got would read the cap as the place having run out.
//
// Zero is nothing asked, since that is what an absent field decodes to. A
// negative is refused rather than read as zero: it is a mistake somewhere in
// the caller, and quietly turning it into the default would answer a question
// that was never put.
func capLimit(asked, byDefault, most int) (limit int, note string, err error) {
	switch {
	case asked < 0:
		return 0, "", errors.Errorf(
			"limit %d is negative: leave it out for %d, or name one up to %d",
			asked, byDefault, most)
	case asked == 0:
		return byDefault, "", nil
	case asked > most:
		return most, "limit " + strconv.Itoa(asked) + " was lowered to " +
			strconv.Itoa(most) + ", which is the most one call answers with", nil
	default:
		return asked, "", nil
	}
}
