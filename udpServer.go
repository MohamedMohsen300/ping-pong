package main

import (
	"fmt"
	"net"
	"sync"
)

const (
	_register = 1
	_ping     = 2
	_message  = 3
)

type Job struct {
	Addr    *net.UDPAddr
	Payload []byte
}

type Client struct {
	ID   string
	Addr *net.UDPAddr
}

type Server struct {
	conn          *net.UDPConn
	clientsByID   map[string]*Client
	clientsByAddr map[string]*Client
	mu            sync.Mutex
	writeQueue    chan Job
}

func NewServer(server string) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		conn:          conn,
		clientsByID:   make(map[string]*Client),
		clientsByAddr: make(map[string]*Client),
		writeQueue:    make(chan Job, 100),
	}
	return s, nil
}

func (s *Server) udpWriteWorker(id int) {
	for {
		job := <-s.writeQueue
		_, err := s.conn.WriteToUDP(job.Payload, job.Addr)
		if err != nil {
			fmt.Printf("Writer %d error: %v\n", id, err)
		}
	}
}

func (s *Server) udpReadWorker() {
	buf := make([]byte, 65507)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}
		packet := buf[:n]
		s.PacketParser(addr, packet)
	}
}

func (s *Server) PacketParser(addr *net.UDPAddr, packet []byte) {
	if len(packet) == 0 {
		return
	}
	msgType := packet[0]
	payload := packet[1:]

	switch msgType {
	case _register:
		s.handleRegister(addr, payload)

	case _ping:
		s.handlePing(addr)

	case _message:
		s.handleMessage(addr, payload)
	}
}

func (s *Server) handleRegister(addr *net.UDPAddr, payload []byte) {
	id := string(payload)
	client := &Client{ID: id, Addr: addr}
	s.mu.Lock()
	s.clientsByID[id] = client
	s.clientsByAddr[addr.String()] = client
	s.mu.Unlock()
	fmt.Println("Registered client:", id, addr)
}

func (s *Server) handlePing(addr *net.UDPAddr) {
	client, ok := s.clientsByAddr[addr.String()]
	if !ok {
		fmt.Println("Ping from unknown client:", addr)
		return
	}
	fmt.Printf("Ping from %s\n", client.ID)
	resp := []byte("pong")
	s.writeQueue <- Job{Addr: addr, Payload: resp}
}

func (s *Server) handleMessage(addr *net.UDPAddr, payload []byte) {
	client, ok := s.clientsByAddr[addr.String()]
	if !ok {
		fmt.Println("Message from unknown client:", addr)
		return
	}
	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scanln(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		if send == "send" {
			s.mu.Lock()
			if client, ok := s.clientsByID[id]; ok {
				s.writeQueue <- Job{Addr: client.Addr, Payload: []byte(msg)}
			} else {
				fmt.Printf("Client %s not found\n", id)
			}
			s.mu.Unlock()
		} else {
			fmt.Println("Unknown command")
		}
	}
}

func (s *Server) Start() {
	for i := 1; i <= 3; i++ {
		go s.udpWriteWorker(i)
	}

	go s.udpReadWorker()
	go s.MessageFromServerAnyTime()

	select {}
}

func main() {
	//173.208.144.109
	server, err := NewServer("173.208.144.109:9000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 9000")
	server.Start()
}
