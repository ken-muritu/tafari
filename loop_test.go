package main

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestFromLinkedDevice(t *testing.T) {
	phone := types.JID{User: "254115594826", Server: types.DefaultUserServer, Device: 0}
	tafari := types.JID{User: "254115594826", Server: types.DefaultUserServer, Device: 3}
	if fromLinkedDevice(true, phone, true) {
		t.Fatal("Ken's own voice note from the phone must be handled")
	}
	if !fromLinkedDevice(true, tafari, true) {
		t.Fatal("a voice note from a linked device (Tafari) must be ignored")
	}
	if fromLinkedDevice(true, tafari, false) {
		t.Fatal("text from a linked device is not affected by this rule")
	}
	if fromLinkedDevice(false, tafari, true) {
		t.Fatal("other people's voice notes are not affected by this rule")
	}
}
