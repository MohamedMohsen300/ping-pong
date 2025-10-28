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

var (
	clients   = make(map[string]*quic.Conn)
	clientsMu sync.RWMutex
)

func addClient(addr string, sess *quic.Conn) {
	clientsMu.Lock()
	clients[addr] = sess
	clientsMu.Unlock()
}

func removeClient(addr string) {
	clientsMu.Lock()
	delete(clients, addr)
	clientsMu.Unlock()
}

func getClient(addr string) (*quic.Conn, bool) {
	clientsMu.RLock()
	s, ok := clients[addr]
	clientsMu.RUnlock()
	return s, ok
}

func listClients() []string {
	clientsMu.RLock()
	out := make([]string, 0, len(clients))
	for k := range clients {
		out = append(out, k)
	}
	clientsMu.RUnlock()
	return out
}

func handleIncomingStreams(sess *quic.Conn, remoteAddr string) {
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			fmt.Printf("session %s accept stream error (closed?): %v\n", remoteAddr, err)
			removeClient(remoteAddr)
			return
		}
		go handleStream(stream)
	}
}

func handleStream(stream *quic.Stream) {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
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

	buffer := make([]byte, 64*1024)
	var total int64
	start := time.Now()
	lastPrint := time.Now()

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			total += int64(n)
			f.Write(buffer[:n])
		}
		if time.Since(lastPrint) > time.Second {
			mb := float64(total) / (1024 * 1024)
			speed := mb / time.Since(start).Seconds()
			fmt.Printf("\r📥 Receiving %s: %.2f MB | Speed: %.2f MB/s", filename, mb, speed)
			lastPrint = time.Now()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("\nerror reading stream:", err)
			return
		}
	}
	fmt.Printf("\n✅ received %s (%.2f MB)\n", outPath, float64(total)/(1024*1024))
}

// ----------------- Send file with tracking -----------------
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

	info, _ := f.Stat()
	fileSize := info.Size()

	buffer := make([]byte, 64*1024)
	var sent int64
	start := time.Now()
	lastPrint := time.Now()

	for {
		n, err := f.Read(buffer)
		if n > 0 {
			stream.Write(buffer[:n])
			sent += int64(n)
		}
		if time.Since(lastPrint) > time.Second {
			percent := float64(sent) / float64(fileSize) * 100
			mbSent := float64(sent) / (1024 * 1024)
			speed := mbSent / time.Since(start).Seconds()
			fmt.Printf("\r📤 Sending %s: %.2f MB (%.1f%%) | Speed: %.2f MB/s", filename, mbSent, percent, speed)
			lastPrint = time.Now()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading file: %w", err)
		}
	}

	fmt.Printf("\n✅ sent %s (%.2f MB) successfully\n", filename, float64(sent)/(1024*1024))
	return nil
}

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
		case line == "list":
			cs := listClients()
			if len(cs) == 0 {
				fmt.Println("no clients connected")
			} else {
				fmt.Println("connected clients:")
				for _, c := range cs {
					fmt.Println(" ", c)
				}
			}
		case strings.HasPrefix(line, "sendfile "):
			rest := strings.TrimPrefix(line, "sendfile ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) != 2 {
				fmt.Println("usage: sendfile <clientAddr> <filename>")
				continue
			}
			clientAddr := strings.TrimSpace(parts[0])
			filename := strings.TrimSpace(parts[1])
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
		case strings.HasPrefix(line, "broadcast "):
			filename := strings.TrimSpace(strings.TrimPrefix(line, "broadcast "))
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
					fmt.Println("broadcasting to", a)
					if err := sendFileOnSession(s, filename); err != nil {
						fmt.Println("broadcast send error to", a, ":", err)
					}
				}(addr, sess)
			}
			clientsMu.RUnlock()
		case line == "quit" || line == "exit":
			fmt.Println("closing all clients and exiting...")
			clientsMu.RLock()
			for addr, sess := range clients {
				sess.CloseWithError(0, "server shutdown")
				fmt.Println("closed", addr)
			}
			clientsMu.RUnlock()
			os.Exit(0)
		default:
			fmt.Println("unknown command — available: list, sendfile, broadcast, quit")
		}
	}
}

func main() {
	addr := ":11000"
	quicConfig := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  2 * time.Minute,
	}

	listener, err := quic.ListenAddr(addr, generateTLSConfig(), quicConfig)
	if err != nil {
		panic(err)
	}
	fmt.Println("QUIC server listening on", addr)
	go stdinCommandLoop()

	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			fmt.Println("accept error:", err)
			continue
		}
		remote := sess.RemoteAddr().String()
		fmt.Println("new client:", remote)
		addClient(remote, sess)
		go handleIncomingStreams(sess, remote)
	}
}
