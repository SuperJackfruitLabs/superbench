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
	if err := c.promote(); err == nil {
		t.Fatal("promoted past normal connection ceiling")
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
