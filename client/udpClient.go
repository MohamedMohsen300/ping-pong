package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"context"
	"github.com/quic-go/quic-go"
)

func clientTLSConfig() *tls.Config {
	// In testing with the self-signed server certificate we skip verification.
	// For production you should verify the server certificate properly.
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-file-transfer"},
	}
}

func sendFile(addr, path string) error {
	ctx := context.Background()
	session, err := quic.DialAddr(ctx, addr, clientTLSConfig(), nil)

	if err != nil {
		return fmt.Errorf("failed to dial QUIC: %w", err)
	}
	defer session.CloseWithError(quic.ApplicationErrorCode(0), "normal close")
	
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	filename := filepath.Base(path)
	// first send header line: filename + '\n'
	_, err = stream.Write([]byte(filename + "\n"))
	if err != nil {
		return fmt.Errorf("failed to send filename header: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	start := time.Now()
	n, err := io.Copy(stream, f)
	if err != nil {
		return fmt.Errorf("failed to copy file to stream: %w", err)
	}
	elapsed := time.Since(start)
	fmt.Printf("sent %d bytes in %v\n", n, elapsed)
	return nil
}

func main() {
	addr := "127.0.0.1:11000"
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("QUIC client ready. Use: sendfile:<path> or exit")

	for {
		fmt.Print("> ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		if strings.HasPrefix(line, "sendfile:") {
			path := strings.TrimPrefix(line, "sendfile:")
			path = strings.TrimSpace(path)
			if path == "" {
				fmt.Println("provide file path after sendfile:")
				continue
			}
			fmt.Println("sending file:", path)
			err := sendFile(addr, path)
			if err != nil {
				fmt.Println("Send error:", err)
			} else {
				fmt.Println("file sent successfully")
			}
			continue
		}
		fmt.Println("unknown command")
	}
}
