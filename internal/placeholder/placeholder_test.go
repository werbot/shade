package placeholder_test

import (
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

func TestFormat(t *testing.T) {
	if got := placeholder.Format("HOST", 1); got != "<HOST_1>" {
		t.Fatalf("got %q", got)
	}
}
