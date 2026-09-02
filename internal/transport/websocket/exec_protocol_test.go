package websockettransport

import "testing"

func TestDecodeClientFrameValidation(t *testing.T) {
	for _, raw := range []string{`{"type":"unknown"}`, `{"type":"resize","rows":0,"cols":80}`, `{"type":"resize","rows":24,"cols":4097}`, `{"type":"stdin","data":"ok","extra":true}`, `{"type":"stdin"}{"type":"stdin"}`} {
		if _, err := decodeClientFrame([]byte(raw), 64); err == nil {
			t.Fatalf("accepted invalid frame %s", raw)
		}
	}
	if _, err := decodeClientFrame([]byte(`{"type":"stdin","data":"12345"}`), 4); err == nil {
		t.Fatal("accepted oversized stdin")
	}
	frame, err := decodeClientFrame([]byte(`{"type":"resize","rows":30,"cols":120}`), 64)
	if err != nil || frame.Rows != 30 || frame.Cols != 120 {
		t.Fatalf("valid resize: %#v %v", frame, err)
	}
}
