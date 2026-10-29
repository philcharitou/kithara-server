package main

import (
	"reflect"
	"testing"
)

// Tags as real rippers write them (these are from a real library).
func TestTagQuirks(t *testing.T) {
	dune := tagMeta(map[string]string{
		"album": "Dune 2꞉ Dune Messiah", "artist": "Frank Herbert", "genre": "Science Fiction",
		"comment": "Read by Scott Brick, Katherine Kellgren, Euan Morton & Simon Vance",
		"lyrics":  "Frank Herbert's second installment explores new developments on the desert planet Arrakis.",
	}, true)
	if dune.Narrator != "Scott Brick, Katherine Kellgren, Euan Morton, Simon Vance" {
		t.Errorf("narrator %q", dune.Narrator)
	}
	if dune.Description != "Frank Herbert's second installment explores new developments on the desert planet Arrakis." {
		t.Errorf("description %q", dune.Description)
	}
	dcc := tagMeta(map[string]string{
		"album": "The Butcher's Masquerade: Dungeon Crawler Carl, Book 5", "composer": "Jeff Hays",
		"comment": "Chapter 80", "genre": "Audiobook",
	}, true)
	if dcc.Description != "" || len(dcc.Genres) != 0 || dcc.Narrator != "Jeff Hays" {
		t.Errorf("placeholder comment or genre kept: %+v", dcc)
	}
}

func TestTidy(t *testing.T) {
	idx := func(f float64) *float64 { return &f }
	for _, c := range []struct {
		in, title, series string
		index             *float64
	}{
		{"The Butcher's Masquerade: Dungeon Crawler Carl, Book 5", "The Butcher's Masquerade", "Dungeon Crawler Carl", idx(5)},
		{"Dune 2꞉ Dune Messiah", "Dune Messiah", "Dune", idx(2)},
		{"Dune 3: Children of Dune", "Children of Dune", "Dune", idx(3)},
		{"Dungeon Crawler Carl (Unabridged)", "Dungeon Crawler Carl", "", nil},
		{"Catch 22: A Novel", "Catch 22: A Novel", "", nil},
		{"Fahrenheit 451", "Fahrenheit 451", "", nil},
	} {
		b := Book{Title: c.in, Chapters: []Chapter{{Title: " One "}}}
		tidy(&b)
		if b.Title != c.title || b.Series != c.series || !reflect.DeepEqual(b.SeriesIndex, c.index) || b.Chapters[0].Title != "One" {
			t.Errorf("%q: got %q / %q / %v", c.in, b.Title, b.Series, b.SeriesIndex)
		}
	}
	// A series the book already has wins over one read out of its title.
	b := Book{Title: "Something: Other Series, Book 2", Series: "Real Series"}
	tidy(&b)
	if b.Series != "Real Series" || b.Title != "Something: Other Series, Book 2" {
		t.Errorf("took a different series from the title: %+v", b)
	}
}

func TestJoinSentences(t *testing.T) {
	got := joinSentences([]Passage{
		{0, 1000, "The transformation occurred at 2.23am, as far as I"},
		{1000, 2000, "could tell."},
		{2100, 3000, "Pretty much anyone indoors was killed."},
		{9000, 9500, "After a long pause"},
		{20000, 21000, "much later."},
	})
	want := []Passage{
		{0, 2000, "The transformation occurred at 2.23am, as far as I could tell."},
		{2100, 3000, "Pretty much anyone indoors was killed."},
		{9000, 9500, "After a long pause"},
		{20000, 21000, "much later."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}
