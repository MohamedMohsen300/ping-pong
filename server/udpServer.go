package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

func generateTLSConfig() *tls.Config {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, _ := rand.Int(rand.Reader, serialNumberLimit)

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"quic-test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"quic-file-transfer"},
	}
}

// ----------------- clients map -----------------
var (
	clients   = make(map[string]*quic.Conn) // key: remoteAddr string
	clientsMu sync.RWMutex
)

// add client
func addClient(addr string, sess *quic.Conn) {
	clientsMu.Lock()
	clients[addr] = sess
	clientsMu.Unlock()
}

// remove client
func removeClient(addr string) {
	clientsMu.Lock()
	delete(clients, addr)
	clientsMu.Unlock()
}

// get client
func getClient(addr string) (*quic.Conn, bool) {
	clientsMu.RLock()
	s, ok := clients[addr]
	clientsMu.RUnlock()
	return s, ok
}

// list clients
func listClients() []string {
	clientsMu.RLock()
	out := make([]string, 0, len(clients))
	for k := range clients {
		out = append(out, k)
	}
	clientsMu.RUnlock()
	return out
}

// ----------------- stream / file helpers -----------------

// handleStream reads filename\n then copies rest to fromPeer_<filename>
func handleStream(stream *quic.Stream) {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
		// probably peer closed early
		if !strings.Contains(err.Error(), "Application error 0x0") {
			fmt.Println("failed to read filename header:", err)
		}
		return
	}
	filename := strings.TrimSpace(line)
	if filename == "" {
		fmt.Println("empty filename header")
		return
	}

	outPath := "fromPeer_" + filename
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Println("failed to create file:", err)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, reader)
	if err != nil && !strings.Contains(err.Error(), "Application error 0x0") {
		fmt.Printf("error while writing file %s: %v\n", outPath, err)
		return
	}
	fmt.Printf("✅ saved file %s (%d bytes)\n", outPath, n)
}

// sendFileOnSession opens stream on sess and sends filename + contents
func sendFileOnSession(sess *quic.Conn, path string) error {
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		return fmt.Errorf("open stream failed: %w", err)
	}
	defer stream.Close()

	filename := filepath.Base(path)
	_, err = stream.Write([]byte(filename + "\n"))
	if err != nil {
		return fmt.Errorf("failed to write filename header: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	start := time.Now()
	n, err := io.Copy(stream, f)
	if err != nil && !strings.Contains(err.Error(), "Application error 0x0") {
		return fmt.Errorf("failed to copy file to stream: %w", err)
	}
	fmt.Printf("📤 sent %s (%d bytes) in %v\n", filename, n, time.Since(start))
	return nil
}

// handleIncomingStreams listens for incoming streams on a session
func handleIncomingStreams(sess *quic.Conn, remoteAddr string) {
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			fmt.Printf("session %s accept stream error (closed?): %v\n", remoteAddr, err)
			removeClient(remoteAddr)
			return
		}
		// pass pointer to avoid copying locks
		go handleStream(stream)
	}
}

// ----------------- stdin command loop (global) -----------------

func stdinCommandLoop() {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("stdin read error:", err)
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch {
		case line == "help":
			fmt.Println("commands:")
			fmt.Println("  list                                - list connected clients")
			fmt.Println("  sendfile:<clientAddr>:<filename>    - send file to specific client")
			fmt.Println("  broadcast:<filename>                - send file to all clients")
			fmt.Println("  disconnect:<clientAddr>             - disconnect a client")
			fmt.Println("  quit / exit                         - stop server")
		case line == "list":
			clients := listClients()
			if len(clients) == 0 {
				fmt.Println("no clients connected")
			} else {
				fmt.Println("connected clients:")
				for _, c := range clients {
					fmt.Println(" ", c)
				}
			}
		case strings.HasPrefix(line, "sendfile:"):
			rest := strings.TrimPrefix(line, "sendfile:")
			parts := strings.SplitN(rest, ":", 2)
			if len(parts) != 2 {
				fmt.Println("usage: sendfile:<clientAddr>:<filename>")
				continue
			}
			clientAddr := strings.TrimSpace(parts[0])
			filename := strings.TrimSpace(parts[1])
			if filename == "" {
				fmt.Println("provide filename")
				continue
			}
			sess, ok := getClient(clientAddr)
			if !ok {
				fmt.Println("no such client:", clientAddr)
				continue
			}
			if _, err := os.Stat(filename); os.IsNotExist(err) {
				fmt.Println("file not found:", filename)
				continue
			}
			if err := sendFileOnSession(sess, filename); err != nil {
				fmt.Println("send error:", err)
			}
		case strings.HasPrefix(line, "broadcast:"):
			filename := strings.TrimSpace(strings.TrimPrefix(line, "broadcast:"))
			if filename == "" {
				fmt.Println("provide filename")
				continue
			}
			if _, err := os.Stat(filename); os.IsNotExist(err) {
				fmt.Println("file not found:", filename)
				continue
			}
			clientsMu.RLock()
			for addr, sess := range clients {
				go func(a string, s *quic.Conn) {
					if err := sendFileOnSession(s, filename); err != nil {
						fmt.Println("broadcast send error to", a, ":", err)
					}
				}(addr, sess)
			}
			clientsMu.RUnlock()
		case strings.HasPrefix(line, "disconnect:"):
			addr := strings.TrimSpace(strings.TrimPrefix(line, "disconnect:"))
			if addr == "" {
				fmt.Println("provide client address")
				continue
			}
			sess, ok := getClient(addr)
			if !ok {
				fmt.Println("no such client:", addr)
				continue
			}
			_ = sess.CloseWithError(quic.ApplicationErrorCode(0), "server disconnect")
			removeClient(addr)
			fmt.Println("disconnected", addr)
		case line == "quit" || line == "exit":
			fmt.Println("shutting down server — closing all sessions")
			// close all sessions
			clientsMu.RLock()
			for addr, sess := range clients {
				_ = sess.CloseWithError(quic.ApplicationErrorCode(0), "server shutting down")
				fmt.Println("closed", addr)
			}
			clientsMu.RUnlock()
			os.Exit(0)
		default:
			fmt.Println("unknown command — type 'help' for usage")
		}
	}
}

// ----------------- main -----------------

func main() {
	addr := ":11000"
	listener, err := quic.ListenAddr(addr, generateTLSConfig(), nil)
	if err != nil {
		panic(err)
	}
	fmt.Println("QUIC server listening on", addr)

	// start stdin loop in background
	go stdinCommandLoop()

	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			fmt.Println("accept error:", err)
			continue
		}
		remote := sess.RemoteAddr().String()
		fmt.Println("new session from", remote)

		// store session
		addClient(remote, sess)

		// handle incoming streams for this session
		go handleIncomingStreams(sess, remote)
	}
}
