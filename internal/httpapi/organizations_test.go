package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLaravelPageWindowSmall(t *testing.T) {
	got := laravelPageWindow(3, 5)
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestLaravelPageWindowSlider(t *testing.T) {
	got := laravelPageWindow(10, 30)
	// UrlWindow onEachSide=3: 1 ... 7,8,9,10,11,12,13 ... 30
	want := []int{1, -1, 7, 8, 9, 10, 11, 12, 13, -1, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSignedInvitationURLRoundTrip(t *testing.T) {
	s := &Server{appURL: "http://tock.test", appKey: "secret"}
	full := s.signInvitationURL("0190abcd-1234-7000-8000-000000000001")
	r := httptest.NewRequest("GET", full, nil)
	if !s.verifyURLSignature(r, true) {
		t.Fatal("expected valid relative signature")
	}
	// Tampering must fail.
	r2 := httptest.NewRequest("GET", full+"&x=1", nil)
	if s.verifyURLSignature(r2, true) {
		t.Fatal("tampered query must not verify")
	}
	// Absolute mode on a relative-signed URL must fail.
	if s.verifyURLSignature(r, false) {
		t.Fatal("relative signature must not verify as absolute")
	}
}

func TestJsonNullableInt(t *testing.T) {
	body, ok := decodeJSONMap(httptest.NewRequest("PUT", "/", strings.NewReader(`{"billable_rate":150,"x":null,"bad":"abc"}`)))
	if !ok {
		t.Fatal("decode failed")
	}
	v, present, valid := jsonNullableInt(body, "billable_rate")
	if !present || !valid || v == nil || *v != 150 {
		t.Fatalf("got v=%v present=%v valid=%v", v, present, valid)
	}
	if _, present, _ := jsonNullableInt(body, "x"); !present {
		t.Fatal("null should count as present")
	}
	if _, _, valid := jsonNullableInt(body, "bad"); valid {
		t.Fatal("string must be invalid")
	}
}
