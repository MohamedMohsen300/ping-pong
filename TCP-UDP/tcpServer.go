package main

import (
	"fmt"
	"net"
)

func tcp() {
	fmt.Println("write port number to listen :")
	var port string
	fmt.Scan(&port)
	address := fmt.Sprintf("173.208.144.109:%s", port)

	listen, err := net.Listen("tcp", address)
	if err != nil {
		panic(err)
	}
	defer listen.Close()
	fmt.Println("TCP Server is listening on Port ", port)

	for {
		// open new session
		conn, err := listen.Accept()
		if err != nil {
			fmt.Println("Error to open new session", err)
			continue
		}
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	defer conn.Close()

	// receive message
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		println(err)
	}

	// send message
	_, err = conn.Write([]byte(string(buf[:n])))
	if err != nil {
		println(err)
	}
}

