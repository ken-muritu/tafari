package main

import (
	"testing"
	"time"
)

func TestHeartbeatStandby(t *testing.T) {
	t0 := time.Now()
	clock := t0
	now = func() time.Time { return clock }
	bootAt = t0
	beatKey = "k"
	lastBeat.Store(0)
	defer func() { now = time.Now; beatKey = ""; lastBeat.Store(0) }()

	if !standbyHolding() {
		t.Fatal("must be silent right after boot")
	}
	clock = t0.Add(46 * time.Second) // no beat ever arrived: primary is down
	if standbyHolding() {
		t.Fatal("must answer when no heartbeat arrives within the timeout")
	}
	lastBeat.Store(clock.UnixNano()) // primary comes back
	if !standbyHolding() {
		t.Fatal("a fresh heartbeat must silence the standby")
	}
	clock = clock.Add(30 * time.Second)
	if !standbyHolding() {
		t.Fatal("a 30s old beat is still fresh")
	}
	clock = clock.Add(20 * time.Second) // 50s since the beat
	if standbyHolding() {
		t.Fatal("a 50s old beat must trigger takeover")
	}
}
