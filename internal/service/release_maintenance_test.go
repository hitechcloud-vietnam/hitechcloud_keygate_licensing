package service

import (
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

func TestReleaseWithinUpdates(t *testing.T) {
	until := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before, after := until.Add(-time.Hour), until.Add(time.Hour)
	cases := []struct {
		name string
		rel  model.Release
		lim  *time.Time
		want bool
	}{
		{"no limit", model.Release{PublishedAt: &after}, nil, true},
		{"published before end", model.Release{PublishedAt: &before}, &until, true},
		{"published at end", model.Release{PublishedAt: &until}, &until, true},
		{"published after end", model.Release{PublishedAt: &after}, &until, false},
		{"no publish date under a cutoff", model.Release{}, &until, false},
		{"no publish date without a cutoff", model.Release{}, nil, true},
	}
	for _, c := range cases {
		if got := releaseWithinUpdates(&c.rel, c.lim); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
