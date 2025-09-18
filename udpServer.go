package main

import (
	"fmt"
	"net"
)

func main() {
	conn, err := net.ListenPacket("udp", "127.0.0.1:5001")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	fmt.Println("UDP server is listening on port 5001")

	buf := make([]byte, 1024)

	for {
		// receive message
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			println(err)
		}

		// send message
		_,err =conn.WriteTo([]byte(string(buf[:n])), addr)
		if err != nil {
			println(err)
		}
	}
}
