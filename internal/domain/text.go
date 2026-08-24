package domain

import "unicode/utf8"

// TruncateRunes shortens s to at most n runes.
//
// Cutting a string with `s[:n]` cuts BYTES, and a byte-slice of UTF-8 lands in
// the middle of a multi-byte character roughly six times out of seven. SQLite
// stores the result happily; PostgreSQL rejects it outright with "invalid byte
// sequence for encoding UTF8". That is not a cosmetic difference: a delivery
// error carrying a Telegram `description`, an SMTP reply, or a webhook body in
// any non-Latin script would fail to save, leave its attempt stuck in
// `sending`, and be requeued by the reaper forever.
//
// Everything that stores or sends user-supplied text goes through here.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// MaxLastErrorRunes bounds the stored error text of a delivery attempt. Long
// enough to hold a real provider error with its context, short enough that a
// channel returning its entire HTML error page does not fill the table.
const MaxLastErrorRunes = 1000

// MaxHeaderRunes bounds a generated email header value.
const MaxHeaderRunes = 200
