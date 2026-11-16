package service

import (
	"net"
	"sync"
	"testing"
)

func TestPeerCloseReleasesExactlyOneSlot(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	c := &peerConn{Conn: a, slot: slots}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Close() }()
	}
	wg.Wait()
	if len(slots) != 0 {
		t.Fatal("connection slot retained")
	}
}

func TestClassificationPromotionCannotStealFullNormalSlot(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	classifiers := make(chan struct{}, 8)
	normal := make(chan struct{}, 64)
	classifiers <- struct{}{}
	for n := 0; n < 64; n++ {
		normal <- struct{}{}
	}
	c := &peerConn{Conn: a, slot: classifiers, normal: normal}
	defer c.Close()
	if err := c.promote(); err == nil || err.Error() != "connection class limit reached" {
		t.Fatal("normal connection ceiling did not refuse promotion", err)
	}
	if len(normal) != 64 || len(classifiers) != 1 {
		t.Fatal("failed promotion changed reservation")
	}
	<-normal
	if err := c.promote(); err != nil {
		t.Fatal(err)
	}
	if len(normal) != 64 || len(classifiers) != 0 {
		t.Fatal("promotion did not transfer reservation")
	}
}

func TestClassificationPromotionAndCloseCannotLeakOrDoubleRelease(t *testing.T) {
	for n := 0; n < 100; n++ {
		a, b := net.Pipe()
		classifiers := make(chan struct{}, 8)
		normal := make(chan struct{}, 64)
		classifiers <- struct{}{}
		c := &peerConn{Conn: a, slot: classifiers, normal: normal}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.promote() }()
		go func() { defer wg.Done(); _ = c.Close() }()
		wg.Wait()
		_ = b.Close()
		if len(classifiers) != 0 || len(normal) != 0 {
			t.Fatal("reservation leaked after close", len(classifiers), len(normal))
		}
	}
}

type delayedCloseConn struct {
	net.Conn
	entered, release chan struct{}
}

func (c delayedCloseConn) Close() error { close(c.entered); <-c.release; return c.Conn.Close() }

func TestCloseDoesNotRecycleReservationBeforeSocketIsClosed(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	slots := make(chan struct{}, 8)
	slots <- struct{}{}
	c := &peerConn{Conn: delayedCloseConn{Conn: a, entered: entered, release: release}, slot: slots}
	done := make(chan struct{})
	go func() { _ = c.Close(); close(done) }()
	<-entered
	reserved := len(slots)
	close(release)
	<-done
	if reserved != 1 || len(slots) != 0 {
		t.Fatal("connection reservation recycled before underlying close", reserved, len(slots))
	}
}
