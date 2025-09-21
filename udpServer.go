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

func (s *Server) HandlePing(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var foundClient *Client
	for _, c := range s.clients {
		if c.Addr.String() == addr.String() {
			foundClient = c
			break
		}
	}

	if foundClient == nil {
		fmt.Println("client not found tried to ping:", addr)
		return
	}

	msg := fmt.Sprintf("pong from server to client %s", foundClient.ID)
	s.conn.WriteToUDP([]byte(msg), addr)
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
		var cmd, id, msg string
		_, err := fmt.Scanln(&cmd, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		if cmd == "send" {
			s.mu.Lock()
			client := s.clients[id]
			s.conn.WriteToUDP([]byte(msg), client.Addr)
			s.mu.Unlock()
		} else {
			fmt.Println("Unknown command:", cmd)
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
		message := string(buf[:n])

		var cmd, id string
		fmt.Sscanf(message, "%s %s", &cmd, &id)

		switch cmd {
		case "register":
			s.HandleRegisterClient(id, addr)
		case "ping":
			s.HandlePing(addr)
		default:
			s.mu.Lock()
			var clientID string
			for _, c := range s.clients {
				if c.Addr.String() == addr.String() {
					clientID = c.ID
					break
				}
			}
			s.mu.Unlock()
			fmt.Printf("Message from client %s: %s\n", clientID, message)
		}
	}
}

func main() {
	server, err := NewServer("173.208.144.109:9000")
	if err != nil {
		panic(err)
	}
	go server.checkConnection()
	go server.MessageFromServerAnyTime()
	server.Start()
}
