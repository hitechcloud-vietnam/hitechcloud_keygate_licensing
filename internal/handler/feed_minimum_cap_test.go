package handler

import (
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
)

// A cutoff-scoped feed must not tell the client to refuse versions it
// cannot obtain: the floor is capped at the newest entitled release.
func TestCapMinimumVersion(t *testing.T) {
	rel := func(v string) *service.FeedRelease { return &service.FeedRelease{Release: &model.Release{Version: v}} }
	entitled := []*service.FeedRelease{rel("1.4.0"), rel("1.3.0")}
	if got := capMinimumVersion("2.0.0", entitled); got != "1.4.0" {
		t.Fatalf("floor beyond the cutoff: got %q want 1.4.0", got)
	}
	if got := capMinimumVersion("1.2.0", entitled); got != "1.2.0" {
		t.Fatalf("floor inside the period must stay: got %q", got)
	}
	if got := capMinimumVersion("1.4.0", entitled); got != "1.4.0" {
		t.Fatalf("floor equal to the newest entitled: got %q", got)
	}
	if got := capMinimumVersion("2.0.0", nil); got != "" {
		t.Fatalf("no entitled release: got %q want none", got)
	}
	if got := capMinimumVersion("", entitled); got != "" {
		t.Fatalf("no floor configured: got %q", got)
	}
}
