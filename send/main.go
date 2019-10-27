package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"net"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

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

func getInterfacesIPv4() (map[string][]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	rest := make(map[string][]net.IP, 0)
	for _, iface := range ifaces {
		iface, err := net.InterfaceByName(iface.Name)
		if err != nil {
			return nil, err
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		ips := make([]net.IP, 0, len(addrs))
		reg := regexp.MustCompile("^(((\\d{1,2})|(1\\d{2})|(2[0-4]\\d)|(25[0-5]))\\.){3}((\\d{1,2})|(1\\d{2})|(2[0-4]\\d)|(25[0-5]))$")
		for _, it := range addrs {
			ipNet := it.String()
			ip := strings.Split(ipNet, "/")[0]
			if reg.MatchString(ip) {
				ips = append(ips, net.ParseIP(ip))
			}
		}
		if _, ok := rest[iface.Name]; !ok && len(ips) > 0 {
			rest[iface.Name] = ips
		}
	}
	return rest, nil
}

// listenLocalByUDP 监听本地UDP端口，并将数据送到缓存中
func listenLocalByUDP(ch chan []byte, localAddr *net.UDPAddr) {
	listener, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		fmt.Printf("listen local addr [%v] error: %v\n", localAddr, err)
		return
	}
	fmt.Printf("Local: <%s> \n", listener.LocalAddr().String())

	for {
		data := make([]byte, defaultMTU)
		n, remoteAddr, err := listener.ReadFromUDP(data[defaultHeaderLength:])
		if err != nil {
			fmt.Printf("error during read: %s", err)
			continue
		}
		fmt.Printf("<%s> %d bytes\n", remoteAddr, n)
		select {
		case ch <- data[:n+defaultHeaderLength]:
			fmt.Println(n, data[:n])
		default:
			fmt.Printf("ch is full!\n")
		}
	}
}

// listenLocalByUDP 监听本地UDP端口，并将数据送到缓存中
func listenLocalByTCP(ch chan []byte, localAddr *net.TCPAddr) {
	listener, err := net.ListenTCP("tcp", localAddr)
	if err != nil {
		fmt.Printf("listen local addr [%v] error: %v\n", localAddr, err)
		return
	}
	fmt.Printf("Local: <%s> \n", listener.Addr().String())

	conn, err := listener.AcceptTCP()
	if err != nil {
		fmt.Printf("accept error: %v\n", err)
		return
	}

	for {
		data := make([]byte, defaultMTU)
		n, err := conn.Read(data)
		if err != nil {
			fmt.Printf("error during read tcp: %v\n", err)
			continue
		}
		fmt.Printf("<%s> %d bytes, time:%d\n", conn.RemoteAddr().String(), n, time.Now().UnixNano())
		select {
		case ch <- data[:n]:
			fmt.Println(n, data[:n])
		default:
			fmt.Printf("ch is full!\n")
		}
	}
}

func calFEC(data [][]byte, dataShards, parityShards int) ([][]byte, error) {
	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, err
	}
	err = enc.Encode(data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sendData(dataShard, parityShard [][]byte) error {
	groupID++
	var idx uint32
	for _, shard := range dataShard {
		binary.BigEndian.PutUint32(shard, (groupID<<8 | idx))
		idx++
		if rand.Int31n(13) == 1 {
			continue
		}
		fmt.Println("send", shard)
		sendN, err := udpConns[0].Write(shard)
		if err != nil {
			return err
		}
		if sendN != len(shard) {
			fmt.Printf("data len = %d != send len = %d!", len(shard), sendN)
		}
	}
	fmt.Println("send data shard complete")
	for _, shard := range parityShard {
		binary.BigEndian.PutUint32(shard, (groupID<<8 | idx))
		idx++
		fmt.Println("send", shard)
		sendN, err := udpConns[1].Write(shard)
		if err != nil {
			return err
		}
		if sendN != len(shard) {
			fmt.Printf("parity len = %d != send len = %d!", len(shard), sendN)
		}
	}
	fmt.Println("send parity shard complete")
	return nil
}

func resharpData(data [][]byte, n int) (int, [][]byte) {
	dataSize := 0
	for i := range data {
		dataSize += len(data[i])
	}
	blockSize := int(math.Ceil(float64(dataSize) / float64(n)))
	result := make([][]byte, 0, n)
	block := make([]byte, blockSize+defaultHeaderLength)
	blockP := defaultHeaderLength
	dataP := 0
	for i := range data {
		for {
			n := copy(block[blockP:], data[i][dataP:])
			fmt.Println(blockP, i, dataP)
			if n < blockSize+defaultHeaderLength-blockP {
				blockP += n
				dataP = 0
				break
			} else {
				result = append(result, block)
				block = make([]byte, blockSize+defaultHeaderLength)
				blockP = defaultHeaderLength
				dataP += n
			}
		}

	}
	return blockSize, result
}

func fecEncodeAndSend(data [][]byte) {
	// 整里数据
	blockSize, data := resharpData(data, dataShards)
	fmt.Printf("block size: %d\n", blockSize)
	for i := range data {
		fmt.Println(data[i])
	}
	// 开辟冗余空间
	for i := 0; i < parityShards; i++ {
		data = append(data, make([]byte, blockSize+defaultHeaderLength))
	}
	// FEC计算
	data, err := calFEC(data, dataShards, parityShards)
	if err != nil {
		fmt.Printf("cal FEC error: %v\n", err)
		return
	}
	// 发送数据
	err = sendData(data[:dataShards], data[dataShards:])
	if err != nil {
		fmt.Printf("send data error: %v\n", err)
		return
	}
}

// cacheData 缓存FEC编码的数据
func cacheData(ch chan []byte) {
	data := make([][]byte, 0, dataShards+parityShards)
	var sum int
	for {
		tmp, ok := <-ch
		if !ok {
			break
		}
		if len(tmp) > 0 {
			fmt.Println(tmp)
			data = append(data, tmp)
			sum += len(tmp)
		}
		if sum > (defaultMTU*(dataShards-1)) || (len(data) > 0 && len(tmp) == 0) {
			go fecEncodeAndSend(data)
			data = make([][]byte, 0, dataShards+parityShards)
			sum = 0
		}
	}
}

func main() {
	// 获取网卡IP地址
	ifaceIPMap, err := getInterfacesIPv4()
	if err != nil {
		fmt.Printf("get interface ips error: %v\n", err)
		return
	}

	// 创建多个UDP发送通道
	for _, iface := range interfacesList {
		srcAddr := &net.UDPAddr{Port: 0}
		dstAddr := &net.UDPAddr{IP: net.ParseIP("192.168.1.40"), Port: 8080}
		if srcIP, ok := ifaceIPMap[iface]; ok {
			srcAddr.IP = srcIP[0]
		} else {
			fmt.Printf("cannot get iface[%s] ipv4 address\n", iface)
			return
		}
		conn, err := net.DialUDP("udp", srcAddr, dstAddr)
		if err != nil {
			fmt.Printf("create udp socket [l:%v, r:%v] error: %v\n", srcAddr, dstAddr, err)
			return
		}
		udpConns = append(udpConns, conn)
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	// 创建本地监听到FEC计算的channel
	ch := make(chan []byte, 100)
	// 缓存并启动FEC计算
	go func() {
		cacheData(ch)
		wg.Done()
	}()

	go func() {
		tick := time.Tick(time.Millisecond * 10)
		for {
			<-tick
			select {
			case ch <- []byte{}:
			default:
				fmt.Println("chan is full")
			}
		}
	}()
	// 创建本地监听端口
	go func() {
		listenLocalByTCP(ch, &net.TCPAddr{IP: net.IPv4zero, Port: defaultListenPort})
		wg.Done()
	}()
	wg.Wait()
}
