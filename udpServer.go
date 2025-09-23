package main

import (
	"fmt"
	"net"
	"sync"
	"time"
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
			now:=time.Now()
			msg := fmt.Sprintf("%v  --->  %v",now.Format("15:04:05"),addr)
			s.conn.WriteToUDP([]byte(msg), addr)
			return
		}
	}
}

func (s *Server) HandleMessage(addr *net.UDPAddr, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.clients {
		if c.Addr.String() == addr.String() {
			fmt.Printf("Message from client %s: %s\n", c.ID, message)
			return
		}
	}
	fmt.Println("Message from unknown client:", addr)
}

func (s *Server) checkConnection() {
	ticker := time.NewTicker(10 * time.Minute)
	for {
		<-ticker.C
		s.mu.Lock()
		for _, client := range s.clients {
			msg := fmt.Sprintf("check connection from server to client %s", client.ID)
			s.conn.WriteToUDP([]byte(msg), client.Addr)
		}
		s.mu.Unlock()
	}
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
				s.conn.WriteToUDP([]byte(msg), client.Addr)
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
	buf := make([]byte, 1024)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}
		messageType := buf[0]
		message := string(buf[1:n])

		switch messageType {
		case 1:
			id := string(buf[1:n])
			s.HandleRegisterClient(id, addr)
		case 2:
			id := string(buf[1:n])
			s.HandlePing(addr, id)
		case 3:
			s.HandleMessage(addr, message)

		default:
			fmt.Println("Unknown message type:", messageType)
		}
	}
}

func main() {
	server, err := NewServer("173.208.144.109:9000")
	fmt.Println("server running on port 9000")
	if err != nil {
		panic(err)
	}
	go server.checkConnection()
	go server.MessageFromServerAnyTime()
	server.Start()
}
