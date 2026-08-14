package main

import (
	"strings"
	"unicode"
)

// normalizeEndpointInput cleans text pasted or typed into an IP field. The
// boolean reports whether a non-empty port was found and should be transferred.
func normalizeEndpointInput(input string) (host, port string, hasPort bool) {
	normalized := normalizeIPInput(input)

	if strings.HasPrefix(normalized, "[") {
		if bracket := strings.Index(normalized, "]"); bracket >= 0 {
			remainder := normalized[bracket+1:]
			if strings.HasPrefix(remainder, ":") && len(remainder) > 1 {
				return normalized[:bracket+1], remainder[1:], true
			}
		}
		return normalized, "", false
	}

	if strings.Count(normalized, ":") == 1 {
		separator := strings.IndexByte(normalized, ':')
		if separator < len(normalized)-1 {
			return normalized[:separator], normalized[separator+1:], true
		}
	}
	return normalized, "", false
}

func normalizeIPInput(input string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\uff0e', '\u3002':
			return '.'
		case '\uff1a':
			return ':'
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, input)
}
