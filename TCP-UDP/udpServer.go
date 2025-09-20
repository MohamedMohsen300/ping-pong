package main

import (
	"fmt"
	"net"
	"time"
)

func udp() {
	fmt.Println("write port number to listen :")
	var port string
	fmt.Scan(&port)
	address := fmt.Sprintf("173.208.144.109:%s", port)

	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	fmt.Println("UDP server is listening on port ", port)

	clientsAddress := make(map[string]net.Addr)
	buf := make([]byte, 1024)

	go func() {
		for {
			// receive message
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				println(err)
			}

			// save client address
			clientsAddress[addr.String()] = addr

			// send message
			_, err = conn.WriteTo([]byte(string(buf[:n])), addr)
			if err != nil {
				println(err)
			}
		}
	}()

	// to send check after 10 min
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			for _, cAddr := range clientsAddress {
				fmt.Println("send check connection")
				conn.WriteTo([]byte("check connection"), cAddr)
			}
		}
	}()

	select {}
}
