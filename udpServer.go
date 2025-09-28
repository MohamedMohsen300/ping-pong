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
	conn       *net.UDPConn
	clients    map[string]*Client
	mu         sync.Mutex
	writeQueue chan Job
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
		conn:       conn,
		clients:    make(map[string]*Client),
		writeQueue: make(chan Job, 100),
	}
	return s, nil
}

func (s *Server) writerWorker(id int) {
	for job := range s.writeQueue {
		_, err := s.conn.WriteToUDP(job.Payload, job.Addr)
		if err != nil {
			fmt.Printf("Writer %d error: %v\n", id, err)
		}
	}
}

func (s *Server) readerWorker() {
	buf := make([]byte, 65507)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}
		s.handlePacket(addr, buf[:n])
	}
}

func (s *Server) handlePacket(addr *net.UDPAddr, data []byte) {
	if len(data) < 1 {
		return
	}
	msgType := data[0]
	payload := data[1:]

	switch msgType {
	case _register:
		id := string(payload)
		s.mu.Lock()
		s.clients[id] = &Client{ID: id, Addr: addr}
		s.mu.Unlock()
		fmt.Println("Registered client:", id, addr)

	case _ping:
		id := string(payload)
		fmt.Printf("Ping from %s\n", id)
		resp := []byte("pong")
		s.writeQueue <- Job{Addr: addr, Payload: resp}

	case _message:
		s.mu.Lock()
		var sender string
		for _, c := range s.clients {
			if c.Addr.String() == addr.String() {
				sender = c.ID
				break
			}
		}
		s.mu.Unlock()
		fmt.Printf("Message from %s: %s\n", sender, string(payload))
	}
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scan(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		if send == "send" {
			s.mu.Lock()
			if client, ok := s.clients[id]; ok {
				s.writeQueue <- Job{Addr: client.Addr, Payload: []byte(msg)}
			} else {
				fmt.Printf("Client %s not found\n", id)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) Start() {
	for i := 1; i <= 3; i++ {
		go s.writerWorker(i)
	}

	go s.readerWorker()
	go s.MessageFromServerAnyTime()

	select {}
}

func main() {
	server, err := NewServer("173.208.144.109:9000")
	if err != nil {
		panic(err)
	}
	fmt.Println("Server running on port 9000")
	server.Start()
}
