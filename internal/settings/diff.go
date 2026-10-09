package settings

import (
	"bytes"
	"strings"
)

// Diff returns a line-based diff of before and after: context lines carry a leading
// space, removals a '-', additions a '+', and equal inputs give an empty string.
//
// There are no @@ hunks: the settings file fits on a screen, and a private hunk
// format would be one more format to test.
func Diff(before, after []byte) string {
	if bytes.Equal(before, after) {
		return ""
	}
	a, b := splitLines(before), splitLines(after)

	// lcs[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var out []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, " "+a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "-"+a[i])
			i++
		default:
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+"+b[j])
	}
	return strings.Join(out, "\n") + "\n"
}

// splitLines splits b into lines. The trailing newline of a file is a terminator,
// not an empty last line, and an empty input has no lines at all.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
