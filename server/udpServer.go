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

// handleIncomingStreams: loop AcceptStream and handle each incoming stream (save file)
func handleIncomingStreams(sess *quic.Conn) {
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			// session closed or error
			fmt.Println("accept stream error (session closed?):", err)
			return
		}
		// pass pointer to avoid copying locks
		go handleStream(stream)
	}
}

// handleStream: read first line filename then copy rest to file
func handleStream(stream *quic.Stream) {
	// Note: stream is *quic.Stream (pointer)
	defer stream.Close()

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("failed to read filename header:", err)
		return
	}
	filename := strings.TrimSpace(line)
	if filename == "" {
		fmt.Println("empty filename, closing stream")
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
	// io.Copy may return error when peer closes with application code 0x0 — ignore that as success
	if err != nil && !strings.Contains(err.Error(), "Application error 0x0") {
		fmt.Printf("error while writing file %s: %v\n", outPath, err)
		return
	}
	fmt.Printf("✅ saved file %s (%d bytes)\n", outPath, n)
}

// sendFileOnSession: open a new stream on sess and send the file located in same folder (path param)
func sendFileOnSession(sess *quic.Conn, path string) error {
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		return fmt.Errorf("open stream failed: %w", err)
	}
	// ensure closing stream when done
	defer stream.Close()

	// filename only (no path) as requested
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
	elapsed := time.Since(start)
	fmt.Printf("📤 sent %s (%d bytes) in %v\n", filename, n, elapsed)
	return nil
}

// stdinCommandLoop: read user commands and perform actions (sendfile:<name>)
func stdinCommandLoop(sess *quic.Conn) {
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
		if line == "quit" || line == "exit" {
			fmt.Println("closing session (server side)")
			// close session gracefully
			_ = sess.CloseWithError(quic.ApplicationErrorCode(0), "server exit")
			return
		}
		if strings.HasPrefix(line, "sendfile:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "sendfile:"))
			if name == "" {
				fmt.Println("provide filename after sendfile:")
				continue
			}
			// file is in same folder as server code
			if _, err := os.Stat(name); os.IsNotExist(err) {
				fmt.Println("file not found:", name)
				continue
			}
			err := sendFileOnSession(sess, name)
			if err != nil {
				fmt.Println("send error:", err)
			}
			continue
		}
		fmt.Println("unknown command")
	}
}

func main() {
	addr := ":11000"
	listener, err := quic.ListenAddr(addr, generateTLSConfig(), nil)
	if err != nil {
		panic(err)
	}
	fmt.Println("QUIC server listening on", addr)
	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			fmt.Println("accept error:", err)
			continue
		}
		fmt.Println("new session from", sess.RemoteAddr())

		// For each accepted session: start incoming-stream handler and stdin command loop
		go handleIncomingStreams(sess)

		// NOTE: stdinCommandLoop reads from os.Stdin; if you expect multiple concurrent sessions,
		// you may want a separate control for which session receives local sendfile commands.
		// For now, we assume one active session at a time and run the stdin loop for it.
		stdinCommandLoop(sess)
		// when stdin loop returns (e.g., "exit"), we continue to accept next session
		fmt.Println("session closed locally, waiting for next connection...")
	}
}
