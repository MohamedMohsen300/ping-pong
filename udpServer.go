package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	_register = 1
	_ping     = 2
	_message  = 3
)

type Client struct {
	ID   string
	Addr *net.UDPAddr
}

type Server struct {
	conn    *net.UDPConn
	clients map[string]*Client
	mu      sync.Mutex
}

func NewServer(port_server string) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", port_server)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		conn:    conn,
		clients: make(map[string]*Client),
	}
	return s, nil
}

func (s *Server) HandleRegisterClient(id string, addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = &Client{ID: id, Addr: addr}
	fmt.Println("Registered client:", id, addr)
}

func (s *Server) HandlePing(addr *net.UDPAddr, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.clients {
		if c.Addr.String() == addr.String() {
			fmt.Printf("pong to client %s \n", id)
			msg := "pong"
			s.conn.WriteToUDP([]byte(msg), addr)
			return
		}
	}
}

func (s *Server) HandleMessage(addr *net.UDPAddr, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//
	parts := strings.SplitN(message, "|", 2)
	if len(parts) != 2 {
		fmt.Println("Invalid message format:", message)
		return
	}
	time_str := parts[0]
	msg := parts[1]

	_time, _ := time.Parse(time.RFC3339Nano, time_str)

	time_taking := time.Since(_time)
	//
	for _, c := range s.clients {
		if c.Addr.String() == addr.String() {
			fmt.Printf("Message from client %s: %s (time taking: %v)\n", c.ID, msg, time_taking)
			fmt.Println("Done, size of msg is ", len(msg))
			return
		}
	}
	fmt.Println("Message from unknown client:", addr)
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
			if client, ok := s.clients[id]; ok {
				if msg == "s" {
					t := time.Now().Format(time.RFC3339Nano)
					text := strings.Repeat("A", 65000)
					message := fmt.Sprintf("%s|%s", t, text)
					s.conn.WriteToUDP([]byte(message), client.Addr)
				} else {
					s.conn.WriteToUDP([]byte(msg), client.Addr)
				}
			} else {
				fmt.Printf("Client %s not found\n", id)
			}
			s.mu.Unlock()
		} else {
			fmt.Println("Unknown command:", send)
		}
	}
}

func (s *Server) Start() {
	buf := make([]byte, 65507)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}
		messageType := buf[0]
		message := string(buf[1:n])

		switch messageType {
		case _register:
			id := string(buf[1:n])
			s.HandleRegisterClient(id, addr)
		case _ping:
			id := string(buf[1:n])
			s.HandlePing(addr, id)
		case _message:
			s.HandleMessage(addr, message)

		default:
			fmt.Println("Unknown message type:", messageType)
		}
	}
}

func main() {
	//173.208.144.109
	server, err := NewServer("173.208.144.109:9000")
	if err != nil {
		panic(err)
	}

	fmt.Println("server running on port 9000")

	go server.MessageFromServerAnyTime()
	server.Start()
}
