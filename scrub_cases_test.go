package baton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The cases are baton-sdk's Scrubber run on generated inputs. Go has no
// redaction counts, so only the outputs are compared.
func TestScrubberMatchesBatonSDKOnTheSpecCases(t *testing.T) {
	f, err := os.Open(filepath.Join("baton-spec", "scrub", "cases.json"))
	if err != nil {
		t.Fatalf("no scrub cases (is the baton-spec submodule checked out?): %v", err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.UseNumber()
	var cases []struct{ Input, Output any }
	if err := d.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no scrub cases")
	}
	failing := 0
	for i, c := range cases {
		if got := DefaultScrubber(c.Input); !reflect.DeepEqual(got, c.Output) {
			failing++
			if failing <= 10 {
				t.Errorf("case %d: input %s\nwant %s\ngot  %s", i, mustJSON(t, c.Input), mustJSON(t, c.Output), mustJSON(t, got))
			}
		}
	}
	if failing > 0 {
		t.Errorf("%d of %d cases differ", failing, len(cases))
	}
}
