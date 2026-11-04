package baton

import (
	"reflect"
	"testing"
)

// A mode value of one setting must not compile in another's field.
func TestEachModeHasItsOwnType(t *testing.T) {
	types := map[reflect.Type]string{
		reflect.TypeOf(ResultCaptureOff):    "ResultCaptureMode",
		reflect.TypeOf(IntentParamOptional): "IntentParamMode",
		reflect.TypeOf(ReportOff):           "ReportMode",
	}
	if len(types) != 3 {
		t.Fatalf("the modes share a type: %v", types)
	}
	for typ, name := range types {
		if typ.Name() != name {
			t.Errorf("got type %q, want %q", typ.Name(), name)
		}
	}
}
