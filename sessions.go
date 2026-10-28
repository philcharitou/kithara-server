package main

import (
	"path/filepath"
	"sort"
	"time"
)

// Listening sessions, worked out on the server from progress uploads.
//
// Version 1 of the protocol keeps statistics on the device, and clients push progress
// when playback pauses, not on a timer. Two uploads in a row for the same book are then
// the edges of a stretch of listening: from position A at the last pause to position B
// at this one. That stretch is recorded, or joined to the session before it when the
// listener picked up again within a few minutes. The result is an estimate, and errs
// on the side of not counting: a jump further than playback could have covered in the
// time between (a seek, a skip, a book marked finished) counts for nothing.

type ListeningSession struct {
	ID              string `json:"id"`
	BookID          string `json:"bookId"`
	Device          string `json:"device,omitempty"`
	StartedAt       int64  `json:"startedAt"`
	EndedAt         int64  `json:"endedAt"`
	StartPositionMs int64  `json:"startPositionMs"`
	EndPositionMs   int64  `json:"endPositionMs"`
	ListenedMs      int64  `json:"listenedMs"` // wall-clock time, so a 2x hour is 30 minutes
}

const (
	sessionJoinGap  = 10 * 60 * 1000     // resume within this of a pause: the same session
	maxStretchMs    = 3 * 60 * 60 * 1000 // longest stretch one pair of uploads may claim
	markFinishedMs  = 30 * 60 * 1000     // a jump into "finished" longer than this is a tap, not listening
	unknownMaxSpeed = 3.0                // when the client does not say its speed
)

// listened estimates the wall-clock listening between two progress records.
func listened(prev, next Progress) int64 {
	gap := next.UpdatedAt - prev.UpdatedAt
	delta := next.PositionMs - prev.PositionMs
	if gap <= 0 || delta <= 0 {
		return 0
	}
	if next.Finished && !prev.Finished && delta > markFinishedMs {
		return 0
	}
	speed, fastest := 1.0, unknownMaxSpeed
	if next.Speed != nil && *next.Speed >= 0.25 && *next.Speed <= 5 {
		speed = *next.Speed
		fastest = speed * 1.1
	}
	// Further than playback could have gone since the last upload: a seek.
	if float64(delta) > float64(gap)*fastest+5000 {
		return 0
	}
	return min(int64(float64(delta)/speed), gap, maxStretchMs)
}

func (s *store) sessionsPath(user string) string {
	return filepath.Join(s.dir, "users", user, "sessions.json")
}

// maxSessions is how many sessions a user's history keeps; older ones drop off.
const maxSessions = 5000

// recordListening adds the stretch between prev and next to the user's sessions. The
// caller holds the user's lock.
func (s *store) recordListening(user, device string, prev, next Progress) error {
	ms := listened(prev, next)
	// A client's clock may be a little ahead; one far ahead is not listening.
	if ms <= 0 || next.UpdatedAt > time.Now().Add(10*time.Minute).UnixMilli() {
		return nil
	}
	var list []ListeningSession
	if err := readJSON(s.sessionsPath(user), &list); err != nil {
		return err
	}
	start := next.UpdatedAt - ms
	// The last session of this book on this device, if this carries straight on from it.
	for i := len(list) - 1; i >= 0 && i >= len(list)-50; i-- {
		cur := &list[i]
		if cur.BookID != next.BookID || cur.Device != device {
			continue
		}
		if cur.EndedAt == prev.UpdatedAt && start-cur.EndedAt <= sessionJoinGap {
			cur.EndedAt, cur.EndPositionMs = next.UpdatedAt, next.PositionMs
			cur.ListenedMs += ms
			return writeJSONCompact(s.sessionsPath(user), list)
		}
		break
	}
	list = append(list, ListeningSession{
		ID: "ls_" + newID(), BookID: next.BookID, Device: device,
		StartedAt: start, EndedAt: next.UpdatedAt,
		StartPositionMs: prev.PositionMs, EndPositionMs: next.PositionMs, ListenedMs: ms,
	})
	if len(list) > maxSessions {
		list = list[len(list)-maxSessions:]
	}
	return writeJSONCompact(s.sessionsPath(user), list)
}

// sessions returns the user's sessions, oldest first, optionally for one book and
// only those ending after since.
func (s *store) sessions(user, bookID string, since int64) ([]ListeningSession, error) {
	defer s.lockUser(user)()
	var list []ListeningSession
	if err := readJSON(s.sessionsPath(user), &list); err != nil {
		return nil, err
	}
	out := []ListeningSession{}
	for _, ls := range list {
		if (bookID == "" || ls.BookID == bookID) && ls.EndedAt > since {
			out = append(out, ls)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, nil
}

// ------------------------------------------------------------------ stats

type Stats struct {
	TotalListenedMs   int64      `json:"totalListenedMs"`
	DaysListened      int        `json:"daysListened"`
	CurrentStreakDays int        `json:"currentStreakDays"`
	LongestStreakDays int        `json:"longestStreakDays"`
	BooksStarted      int        `json:"booksStarted"`
	BooksFinished     int        `json:"booksFinished"`
	Days              []DayStat  `json:"days"`
	Books             []BookStat `json:"books"`
}

type DayStat struct {
	Date       string `json:"date"` // YYYY-MM-DD in the asked-for time zone
	ListenedMs int64  `json:"listenedMs"`
}

type BookStat struct {
	BookID         string `json:"bookId"`
	ListenedMs     int64  `json:"listenedMs"`
	LastListenedAt int64  `json:"lastListenedAt"`
	FinishedAt     int64  `json:"finishedAt,omitempty"`
}

// buildStats sums sessions by day and by book. Days are counted in the zone tz (minutes
// east of UTC), as the listener sees them; a session counts on the day it began.
func buildStats(sessions []ListeningSession, progress []Progress, tz int, now time.Time) Stats {
	zone := time.FixedZone("", tz*60)
	st := Stats{Days: []DayStat{}, Books: []BookStat{}}
	byDay := map[string]int64{}
	byBook := map[string]*BookStat{}
	for _, ls := range sessions {
		st.TotalListenedMs += ls.ListenedMs
		byDay[time.UnixMilli(ls.StartedAt).In(zone).Format(time.DateOnly)] += ls.ListenedMs
		b := byBook[ls.BookID]
		if b == nil {
			b = &BookStat{BookID: ls.BookID}
			byBook[ls.BookID] = b
		}
		b.ListenedMs += ls.ListenedMs
		b.LastListenedAt = max(b.LastListenedAt, ls.EndedAt)
	}
	for _, p := range progress {
		if p.PositionMs > 0 || p.Finished {
			st.BooksStarted++
		}
		if p.Finished {
			st.BooksFinished++
			if b := byBook[p.BookID]; b != nil {
				b.FinishedAt = p.FinishedAt
			}
		}
	}
	for d, ms := range byDay {
		st.Days = append(st.Days, DayStat{Date: d, ListenedMs: ms})
	}
	sort.Slice(st.Days, func(i, j int) bool { return st.Days[i].Date < st.Days[j].Date })
	for _, b := range byBook {
		st.Books = append(st.Books, *b)
	}
	sort.Slice(st.Books, func(i, j int) bool { return st.Books[i].LastListenedAt > st.Books[j].LastListenedAt })

	// Streaks: runs of consecutive days. The current one may end today or yesterday
	// (today's listening may simply not have happened yet).
	st.DaysListened = len(st.Days)
	run := 0
	var last time.Time
	for _, d := range st.Days {
		day, _ := time.ParseInLocation(time.DateOnly, d.Date, zone)
		if run > 0 && day.Sub(last) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		last = day
		st.LongestStreakDays = max(st.LongestStreakDays, run)
	}
	if run > 0 {
		today, _ := time.ParseInLocation(time.DateOnly, now.In(zone).Format(time.DateOnly), zone)
		if gap := today.Sub(last); gap == 0 || gap == 24*time.Hour {
			st.CurrentStreakDays = run
		}
	}
	return st
}
