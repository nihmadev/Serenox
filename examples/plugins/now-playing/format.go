package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNameBytes is Telegram's bound for first_name and last_name. It counts
// bytes, so Cyrillic text fits roughly half as many characters as ASCII.
const maxNameBytes = 64

const ellipsis = "…"

type formatter struct {
	withArtist    string
	withoutArtist string
}

// render returns the last-name text for a track, or "" when the track carries
// nothing displayable.
func (f formatter) render(track Track) string {
	title, artist, album := clean(track.Title), clean(strings.Join(track.Artists, ", ")), clean(track.Album)
	if title == "" {
		return ""
	}
	template := f.withArtist
	if artist == "" {
		template = f.withoutArtist
	}
	value := strings.NewReplacer("{title}", title, "{artist}", artist, "{album}", album).Replace(template)
	return truncate(clean(value), maxNameBytes)
}

// clean drops control characters and collapses whitespace. Telegram trims and
// normalises names server-side, and the core requires the echoed name to match
// what was sent exactly, so the plugin must send the normalised form itself.
func clean(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// truncate shortens value to at most limit bytes without splitting a rune and
// marks the cut with an ellipsis.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	budget := limit - len(ellipsis)
	cut := 0
	for index, r := range value {
		size := utf8.RuneLen(r)
		if index+size > budget {
			break
		}
		cut = index + size
	}
	return strings.TrimRightFunc(value[:cut], unicode.IsSpace) + ellipsis
}
