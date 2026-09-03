package api

import (
	"strings"
	"testing"

	"github.com/biswas-dev/pool/internal/ai"
	"github.com/biswas-dev/pool/internal/store"
)

// A reading the sheet did not carry must stay null, not zero: "no stabilizer
// in the water" and "nobody measured the stabilizer" produce completely
// different dosing advice.
func TestApplyReadingLeavesUnreadFieldsNull(t *testing.T) {
	var test store.Test
	applyReading(&test, map[string]float64{
		"ph":            7.3,
		"free_chlorine": 0,
		"total_salt":    2161,
	})

	if test.PH == nil || *test.PH != 7.3 {
		t.Errorf("ph = %v, want 7.3", test.PH)
	}
	// Zero is a reading. It has to survive, or a chlorine crash reads as an
	// untested row and the analysis says nothing about the thing that matters.
	if test.FreeChlorine == nil {
		t.Error("a free chlorine reading of 0 was dropped as if it were absent")
	} else if *test.FreeChlorine != 0 {
		t.Errorf("free_chlorine = %v, want 0", *test.FreeChlorine)
	}
	if test.TotalSalt == nil || *test.TotalSalt != 2161 {
		t.Errorf("total_salt = %v, want 2161", test.TotalSalt)
	}
	for name, got := range map[string]*float64{
		"cyanuric_acid":    test.CyanuricAcid,
		"calcium_hardness": test.CalciumHardness,
		"temperature":      test.Temperature,
		"iron":             test.Iron,
	} {
		if got != nil {
			t.Errorf("%s = %v, want nil — the sheet did not carry it", name, *got)
		}
	}
}

// Every storable reading needs a home on the test, or a sheet that carries it
// loses it silently.
func TestApplyReadingCoversEveryField(t *testing.T) {
	values := map[string]float64{}
	for _, field := range ai.Fields() {
		values[field] = 1
	}
	var test store.Test
	applyReading(&test, values)

	// Values() is the map the chemistry engine reads, so anything missing from
	// it would be invisible downstream. wqi is carried separately.
	for key, got := range test.Values() {
		if _, offered := values[key]; offered && got == nil {
			t.Errorf("%s was on the sheet but never reached the test", key)
		}
	}
	if test.WQI == nil {
		t.Error("wqi was on the sheet but never reached the test")
	}
}

func TestTranscriptionNote(t *testing.T) {
	if note := transcriptionNote(&ai.SheetReading{}); note != "" {
		t.Errorf("a clean transcription produced a note: %q", note)
	}

	note := transcriptionNote(&ai.SheetReading{
		Notes:      "The salt row is cut off.",
		Rejected:   []string{"ph=73"},
		Model:      "test-model",
		Confidence: 0.8,
	})
	for _, want := range []string{"test-model", "80%", "salt row", "ph=73"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
}

func TestIsFalse(t *testing.T) {
	for _, off := range []string{"false", "0", "no", "OFF", " false "} {
		if !isFalse(off) {
			t.Errorf("isFalse(%q) = false, want true", off)
		}
	}
	// Anything else leaves the analysis on, including the empty string: the
	// caller has to ask for it to be skipped.
	for _, on := range []string{"", "true", "1", "yes", "please"} {
		if isFalse(on) {
			t.Errorf("isFalse(%q) = true, want false", on)
		}
	}
}

func TestIsTrueIsOffByDefault(t *testing.T) {
	for _, on := range []string{"true", "1", "yes", "ON", " true "} {
		if !isTrue(on) {
			t.Errorf("isTrue(%q) = false, want true", on)
		}
	}
	// A dry run has to be asked for. Anything unrecognised — including the
	// empty string — stores the test, which is the documented default.
	for _, off := range []string{"", "false", "0", "no", "maybe"} {
		if isTrue(off) {
			t.Errorf("isTrue(%q) = true, want false", off)
		}
	}
}
