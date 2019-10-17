package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"testing"
	"time"
)

func Test_main(t *testing.T) {
	srcAddr := &net.UDPAddr{
		IP:   net.IPv6zero,
		Port: 0,
	}
	dstAddr := &net.UDPAddr{
		IP:   net.ParseIP("::1"),
		Port: defaultListenPort,
	}
	conn, err := net.DialUDP("udp", srcAddr, dstAddr)
	if err != nil {
		t.Errorf("connect error = %v", err)
	}
	defer conn.Close()
	fmt.Printf("<%s>\n", conn.RemoteAddr())
	for i := 0; i < dataShards; i++ {
		buffer := make([]byte, 8)
		binary.BigEndian.PutUint64(buffer, rand.Uint64())
		writeN, err := conn.Write(buffer)
		if err != nil {
			t.Errorf("conn write error = %v", err)
		}
		fmt.Printf("send %d bytes: %v\n", writeN, buffer)
	}
	time.Sleep(1)
}
