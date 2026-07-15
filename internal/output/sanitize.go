package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func SanitizeTerminal(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			out.WriteString("<INVALID-UTF8>")
			value = value[1:]
			continue
		}
		value = value[size:]
		switch r {
		case '\n':
			out.WriteRune(r)
		case '\t':
			out.WriteString("<TAB>")
		case '\r':
			out.WriteString("<CR>")
		case 0x1b:
			out.WriteString("<ESC>")
		case 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069:
			fmt.Fprintf(&out, "<BIDI-U+%04X>", r)
		default:
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				fmt.Fprintf(&out, "<CTRL-U+%04X>", r)
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}
