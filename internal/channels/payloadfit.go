package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// fitPayload renders an event payload, indented, in at most budget runes.
//
// Cutting the indented text at the limit, as the channels used to, leaves a
// reader holding half a JSON document: an array that stops mid-object, a string
// with no closing quote. A digest whose payload lists thirty places is exactly
// the alert where that happens. So a payload that does not fit is shrunk by its
// structure instead: every array keeps its first items and ends with a
// "… N more" element, then, if that is not enough, long strings are shortened.
// The result is still valid JSON, and the reader sees what was left out.
//
// A payload that is not JSON, or that even the smallest shape cannot fit, is
// cut as plain text with truncatedMark. A budget too small for the mark itself
// gives "".
func fitPayload(raw json.RawMessage, budget int) string {
	return fitJSON(raw, budget, prettyJSON)
}

// fitPayloadCompact is fitPayload for a channel that shows the payload on one
// line (a Teams TextBlock drops the indentation).
func fitPayloadCompact(raw json.RawMessage, budget int) string {
	return fitJSON(raw, budget, compactJSON)
}

// fitJSON is fitPayload with the layout given by format.
func fitJSON(raw json.RawMessage, budget int, format func(json.RawMessage) string) string {
	text := format(raw)
	if utf8.RuneCountInString(text) <= budget {
		return text
	}
	if budget < utf8.RuneCountInString(truncatedMark) {
		return ""
	}
	if !json.Valid(raw) || jsonDepth(raw) > shrinkDepthMax {
		return clip(text, budget)
	}
	fits := func(s string) bool { return utf8.RuneCountInString(s) <= budget }
	render := func(keep, strMax int) string { return format(shrinkJSON(raw, keep, strMax)) }
	// Strings are shortened only when cutting arrays is not enough: a reader
	// loses less from a shorter list than from a cut exception message. No
	// array has more items, and no string more runes, than the payload has
	// bytes, so len(raw) bounds both searches.
	for _, strMax := range []int{-1, 300, 80, 20} {
		if !fits(render(0, strMax)) {
			continue
		}
		keep := largest(0, len(raw), func(k int) bool { return fits(render(k, strMax)) })
		if strMax >= 0 {
			// The step is only a floor: the strings take whatever room the kept
			// items leave, so one long stack trace is not cut to 300 runes in a
			// message with room for thousands.
			strMax = largest(strMax, len(raw), func(m int) bool { return fits(render(keep, m)) })
		}
		return render(keep, strMax)
	}
	return clip(text, budget)
}

// shrinkDepthMax is the deepest payload fitJSON shrinks; a deeper one is cut as
// text. shrinkJSON decodes each value again on every level above it, and the
// indented text grows with the square of the depth, so a payload nested
// thousands of levels deep would cost seconds of CPU on every send. Real
// payloads are a few levels deep.
const shrinkDepthMax = 32

// jsonDepth is how deeply arrays and objects nest in raw, which must be valid
// JSON.
func jsonDepth(raw json.RawMessage) int {
	depth, deepest := 0, 0
	inString, escaped := false, false
	for _, c := range raw {
		switch {
		case escaped:
			escaped = false
		case inString:
			escaped = c == '\\'
			inString = c != '"'
		case c == '"':
			inString = true
		case c == '[' || c == '{':
			depth++
			deepest = max(deepest, depth)
		case c == ']' || c == '}':
			depth--
		}
	}
	return deepest
}

// shrinkJSON returns raw with every array cut to its first keep items, followed
// by a "… N more" string, and, when strMax >= 0, every string longer than
// strMax runes clipped. Object keys keep their order. raw must be valid JSON.
func shrinkJSON(raw json.RawMessage, keep, strMax int) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return raw
	}
	switch raw[0] {
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return raw
		}
		var b bytes.Buffer
		b.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				b.WriteByte(',')
			}
			if i == keep {
				b.Write(jsonString(fmt.Sprintf("… %d more", len(items)-keep)))
				break
			}
			b.Write(shrinkJSON(item, keep, strMax))
		}
		b.WriteByte(']')
		return b.Bytes()
	case '{':
		// Decoded token by token: a map would sort the keys, and the reader
		// would see the fields in a different order than in a payload that fit.
		dec := json.NewDecoder(bytes.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			return raw
		}
		var b bytes.Buffer
		b.WriteByte('{')
		for i := 0; dec.More(); i++ {
			tok, err := dec.Token()
			key, ok := tok.(string)
			if err != nil || !ok {
				return raw
			}
			var value json.RawMessage
			if dec.Decode(&value) != nil {
				return raw
			}
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(jsonString(key))
			b.WriteByte(':')
			b.Write(shrinkJSON(value, keep, strMax))
		}
		b.WriteByte('}')
		return b.Bytes()
	case '"':
		if strMax < 0 {
			return raw
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || utf8.RuneCountInString(s) <= strMax {
			return raw
		}
		return jsonString(clip(s, strMax))
	}
	return raw
}

// jsonString encodes s as a JSON string without json.Marshal's HTML escaping:
// "<" in an exception message must read as "<", not "<".
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// largest is the largest n in [lo, hi] for which ok holds, given that ok(lo)
// does and that ok turns false at most once as n grows.
func largest(lo, hi int, ok func(int) bool) int {
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if ok(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
