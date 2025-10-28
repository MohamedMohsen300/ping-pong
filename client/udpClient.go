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

// ----------------- handle file from server -----------------
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

	buffer := make([]byte, 64*1024) // 64KB buffer
	var totalBytes int64
	start := time.Now()
	lastPrint := time.Now()

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			totalBytes += int64(n)
			f.Write(buffer[:n])
		}
		// progress print every second
		if time.Since(lastPrint) > time.Second {
			mb := float64(totalBytes) / (1024 * 1024)
			speed := mb / time.Since(start).Seconds()
			fmt.Printf("\r📥 Received %.2f MB | Speed: %.2f MB/s", mb, speed)
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
	elapsed := time.Since(start)
	fmt.Printf("\n✅ received %s (%.2f MB) in %.2fs\n", outPath, float64(totalBytes)/(1024*1024), elapsed.Seconds())
}

// ----------------- send file to server -----------------
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

	fileInfo, _ := f.Stat()
	fileSize := fileInfo.Size()
	buffer := make([]byte, 64*1024) // 64KB chunks

	start := time.Now()
	var sent int64
	lastPrint := time.Now()

	for {
		n, err := f.Read(buffer)
		if n > 0 {
			stream.Write(buffer[:n])
			sent += int64(n)
		}
		// print progress every second
		if time.Since(lastPrint) > time.Second {
			percent := float64(sent) / float64(fileSize) * 100
			mbSent := float64(sent) / (1024 * 1024)
			speed := mbSent / time.Since(start).Seconds()
			fmt.Printf("\r📤 Sent: %.2f MB (%.1f%%) | Speed: %.2f MB/s", mbSent, percent, speed)
			lastPrint = time.Now()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("\nerror reading file:", err)
			break
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("\n✅ sent %s (%.2f MB) in %.2fs\n", filename, float64(sent)/(1024*1024), elapsed.Seconds())
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
