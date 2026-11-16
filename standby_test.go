package main

import "testing"

func TestStandbyTakesOverAfterThreeFailuresAndYieldsBack(t *testing.T) {
	primaryDown.Store(false)
	standbyURL = "http://primary.invalid/primary"
	defer func() { standbyURL = ""; primaryDown.Store(false) }()

	fails := 0
	if !standbyHolding() {
		t.Fatal("standby must start silent")
	}
	checkPrimary(&fails, false)
	checkPrimary(&fails, false)
	if !standbyHolding() {
		t.Fatal("two failures must not trigger takeover")
	}
	checkPrimary(&fails, true) // a success resets the count
	checkPrimary(&fails, false)
	checkPrimary(&fails, false)
	if !standbyHolding() {
		t.Fatal("count must reset after a success")
	}
	checkPrimary(&fails, false)
	if standbyHolding() {
		t.Fatal("three failures in a row must take over")
	}
	checkPrimary(&fails, true)
	if !standbyHolding() {
		t.Fatal("primary recovery must silence the standby again")
	}
}

func TestNoStandbyURLMeansAlwaysAnswer(t *testing.T) {
	standbyURL = ""
	if standbyHolding() {
		t.Fatal("a primary copy must never hold")
	}
}
