package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
)

func clientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-file-transfer"},
	}
}

func handleIncomingStreams(sess *quic.Conn) {
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			fmt.Println("accept stream error:", err)
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
		fmt.Println("failed to read filename header:", err)
		return
	}
	filename := strings.TrimSpace(line)
	if filename == "" {
		fmt.Println("empty filename header")
		return
	}

	outPath := "fromServer_" + filename
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Println("failed to create file:", err)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, reader)
	if err != nil && !strings.Contains(err.Error(), "Application error 0x0") {
		fmt.Println("error writing file:", err)
		return
	}
	fmt.Printf("received %s (%d bytes)\n", outPath, n)
}

func sendFile(sess *quic.Conn, path string) error {
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
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
		return fmt.Errorf("failed to copy file: %w", err)
	}
	elapsed := time.Since(start)
	fmt.Printf("sent %s (%d bytes) in %v\n", filename, n, elapsed)
	return nil
}

func main() {
	addr := "173.208.144.109:11000"

	quicConfig := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  2 * time.Minute,
	}

	sess, err := quic.DialAddr(context.Background(), addr, clientTLSConfig(), quicConfig)
	if err != nil {
		fmt.Println("failed to connect:", err)
		return
	}
	defer sess.CloseWithError(0, "client closed")

	fmt.Println("connected to server", addr)
	go handleIncomingStreams(sess)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			fmt.Println("closing...")
			sess.CloseWithError(0, "bye")
			return
		}
		if strings.HasPrefix(line, "sendfile ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "sendfile "))
			if path == "" {
				fmt.Println("provide filename after sendfile ")
				continue
			}
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Println("file not found:", path)
				continue
			}
			err := sendFile(sess, path)
			if err != nil {
				fmt.Println("send error:", err)
			}
			continue
		}
		fmt.Println("unknown command")
	}
}
