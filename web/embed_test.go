package web_test

import (
	"io/fs"
	"testing"

	"github.com/werbot/shade/web"
)

// .gitkeep is the one file dist/ holds on every checkout, built or not. Seen at the root of
// Dist, not under dist/, it shows that index.html will be at the root too.
func TestDistIsRootedAtDist(t *testing.T) {
	if _, err := fs.Stat(web.Dist(), ".gitkeep"); err != nil {
		t.Fatalf("Dist is not rooted at dist/: %v", err)
	}
}
