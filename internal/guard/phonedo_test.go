package guard

import "testing"

// Something done on the person's phone is never covered by "always allow", and the Rules page can set it.
func TestPhoneDoIsNeverRememberedByDefault(t *testing.T) {
	found := false
	for _, n := range DefaultNoRemember {
		found = found || n == "phone_do"
	}
	if !found {
		t.Fatal("phone_do can be set to always allowed")
	}
	if _, ok := CategoryByID("phone_do"); !ok {
		t.Fatal("the Rules page cannot set phone_do")
	}
}
