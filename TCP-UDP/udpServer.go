package main

import (
	"fmt"
	"net"
)

func udp() {
	addr,_ := net.ResolveUDPAddr("udp","0.0.0.0:9001")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	fmt.Println("UDP server is listening on port 9001")

	buf := make([]byte, 1024)

	for {
		// receive message
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			println(err)
		}

		// send message
		_,err =conn.WriteTo([]byte(string(buf[:n])), addr)
		fmt.Println(addr)
		if err != nil {
			println(err)
		}
	}
}
