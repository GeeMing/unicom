// +build windows

package serialwin

import (
	"testing"
)

func TestScoreSample(t *testing.T) {
	// Empty data should return 0
	if s := ScoreSample(nil, 0); s != 0 {
		t.Fatalf("expected 0 for empty data, got %f", s)
	}

	// Clean ASCII without errors
	asciiData := []byte("Hello, World! 123\r\n")
	scoreAscii := ScoreSample(asciiData, 0)
	if scoreAscii < 0.9 {
		t.Fatalf("expected high score for clean ASCII, got %f", scoreAscii)
	}

	// Framing error should heavily penalize
	scoreFraming := ScoreSample(asciiData, 0x0008)
	if scoreFraming > 0.05 {
		t.Fatalf("expected very low score for framing error, got %f", scoreFraming)
	}

	// Parity error should penalize
	scoreParity := ScoreSample(asciiData, 0x0004)
	if scoreParity > 0.1 {
		t.Fatalf("expected low score for parity error, got %f", scoreParity)
	}

	// Garbage data should score lower than clean ASCII
	garbageData := []byte{0x00, 0xFF, 0x01, 0xFE, 0x02, 0xFD}
	scoreGarbage := ScoreSample(garbageData, 0)
	if scoreGarbage >= scoreAscii {
		t.Fatalf("expected garbage score (%f) to be lower than ASCII score (%f)", scoreGarbage, scoreAscii)
	}
}
