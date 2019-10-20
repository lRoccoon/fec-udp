package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"reflect"
	"sync"
	"syscall"

	"github.com/klauspost/reedsolomon"
)

const (
	dataShards          int = 10
	parityShards        int = 2
	defaultMTU          int = 1518
	defaultHeaderLength int = 3 + 1
	defaultListenPort   int = 9981
)

var (
	interfacesList        = []string{"ens33", "ens34"}
	udpConns              = make([]*net.UDPConn, 0, 2)
	groupID        uint32 = 0
	oldGroupID     uint32 = 0
)

func bindToDevice(conn net.PacketConn, device string) error {
	ptrVal := reflect.ValueOf(conn)
	val := reflect.Indirect(ptrVal)
	//next line will get you the net.netFD
	netFdPtr := val.FieldByName("fd")
	val1 := reflect.Indirect(netFdPtr)
	//next line will get you the poll.FD
	pollFdPtr := val1.FieldByName("pfd")
	val2 := reflect.Indirect(pollFdPtr)
	sysFdPtr := val2.FieldByName("Sysfd")
	fd := int(sysFdPtr.Int())
	//fd now has the actual fd for the socket
	return syscall.SetsockoptString(fd, syscall.SOL_SOCKET,
		syscall.SO_BINDTODEVICE, device)
}

func listenRemote(ch chan []byte, listenAddr *net.UDPAddr) {
	listener, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		fmt.Printf("listen udp addr error: %v\n", err)
		return
	}
	fmt.Printf("Local: <%s> \n", listener.LocalAddr().String())
	for {
		data := make([]byte, defaultMTU)
		readN, remoteAddr, err := listener.ReadFromUDP(data)
		if err != nil {
			fmt.Printf("error during read remote: %s", err)
			continue
		}
		fmt.Printf("<%s> %d bytes\n", remoteAddr, readN)
		select {
		case ch <- data[:readN]:
			fmt.Println(readN, data[:readN])
		default:
			fmt.Printf("ch is full!\n")
		}
	}
}

func decodeFEC(data [][]byte, dataShards, parityShards int) ([][]byte, error) {
	fmt.Println(len(data))
	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, err
	}
	err = enc.Reconstruct(data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func cacheFECDecodeData(ch chan []byte) {
	data := make([][]byte, dataShards+parityShards)
	var cnt int
	var idx uint32
	for {
		tmp, ok := <-ch
		if !ok {
			break
		}
		// TODO: 读取组内序号
		id := binary.BigEndian.Uint32(tmp[:defaultHeaderLength])
		idx = id & 0xff
		newGroupID := (id & 0xffffff00) >> 8
		if newGroupID == oldGroupID {
			continue
		}
		if groupID != newGroupID {
			data = make([][]byte, dataShards+parityShards)
			oldGroupID = groupID
			groupID = newGroupID
			cnt = 0
		}

		data[idx] = tmp[defaultHeaderLength:]
		cnt++

		if cnt == dataShards {
			// 判断是否需要解码
			flag := true
			for i := 0; i < dataShards; i++ {
				if data[i] == nil {
					flag = false
					break
				}
			}
			// FEC解码
			if !flag {
				_, err := decodeFEC(data, dataShards, parityShards)
				if err != nil {
					fmt.Printf("decode error: %v\n", err)
					continue
				}
			}
			// 展示解码后的内容
			for _, shard := range data {
				fmt.Println(shard)
			}

		}
	}
}

func main() {

	wg := &sync.WaitGroup{}
	wg.Add(1)

	// 创建UDP到FEC解码的channel
	ch := make(chan []byte, 100)

	// 缓存并启动FEC解码
	go func() {
		cacheFECDecodeData(ch)
		wg.Done()
	}()

	// 监听UDP
	go func() {
		listenRemote(ch, &net.UDPAddr{IP: net.IPv4zero, Port: 8080})
		wg.Done()
	}()

	// 等待协程退出
	wg.Wait()
}
