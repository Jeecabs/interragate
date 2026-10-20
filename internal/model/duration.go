package model

import "fmt"

// Roughly words a duration in seconds the way interragate says how long
// things take: under a minute, then minutes, and past an hour the nearest
// five minutes.
func Roughly(seconds int) string {
	if seconds < 60 {
		return "under a minute"
	}
	m := (seconds + 30) / 60
	if m < 60 {
		return "about " + count(m, "minute")
	}
	m = (m + 2) / 5 * 5
	out := "about " + count(m/60, "hour")
	if m%60 > 0 {
		out += " " + count(m%60, "minute")
	}
	return out
}

// Clock writes seconds from the start of the apply as m:ss, or h:mm:ss.
func Clock(seconds int) string {
	h, m, s := seconds/3600, seconds%3600/60, seconds%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
