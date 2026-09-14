package main

import "testing"

func TestProbeRecoveryBoundary(t *testing.T) {
	original := []byte(`{"input":[{"role":"user","content":"first"}]}`)
	a, ok := probeUserBoundary(original)
	if !ok {
		t.Fatal("user missing")
	}
	same, _ := probeUserBoundary([]byte(`{"model":"changed","input":[{"content":"first","role":"user"},{"type":"reasoning","summary":[]}]}`))
	if a != same {
		t.Fatal("tool/metadata followup mistaken for new input")
	}
	b, ok := probeUserBoundary([]byte(`{"input":[{"role":"user","content":"first"},{"role":"user","content":"new instruction"}]}`))
	if !ok || a == b {
		t.Fatal("new boundary missing")
	}
	if _, ok := probeUserBoundary([]byte(`{"input":[]}`)); ok {
		t.Fatal("empty input accepted")
	}
}
