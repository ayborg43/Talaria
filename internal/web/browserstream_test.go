package web

import (
	"bytes"
	"strings"
	"testing"
)

// The live view is shown by a plain image, which draws a picture only when the next part's header has arrived.
func TestEachPictureIsFollowedAtOnceByTheHeaderOfTheNext(t *testing.T) {
	var b bytes.Buffer
	if err := openStream(&b, "bnd"); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "--bnd\r\nContent-Type: image/jpeg\r\n\r\n" {
		t.Fatalf("the stream opens with %q", got)
	}
	b.Reset()
	if err := writeFrame(&b, "bnd", []byte("JPEGBYTES")); err != nil {
		t.Fatal(err)
	}
	want := "JPEGBYTES\r\n--bnd\r\nContent-Type: image/jpeg\r\n\r\n"
	if b.String() != want {
		t.Fatalf("a picture is sent as %q, want %q", b.String(), want)
	}
	if strings.Contains(b.String(), "Content-Length") {
		t.Fatal("a length was promised that the next picture may not keep")
	}
}
