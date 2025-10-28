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
	"strings"
	"time"

	"github.com/quic-go/quic-go"
)

func generateTLSConfig() *tls.Config {
	// generate a self-signed certificate for testing
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

func handleSession(sess *quic.Conn) {
	defer sess.CloseWithError(0, "")
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			// connection closed or error
			return
		}

		go handleStream(stream)
	}
}

func handleStream(stream *quic.Stream) {
	defer stream.Close()

	// protocol: first line -> filename (terminated by '\n')
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

	outPath := "fromClient_" + filename
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Println("failed to create file:", err)
		return
	}
	defer f.Close()

	// copy the rest of the stream to file
	n, _ := io.Copy(f, reader)
	// if err != nil {
	// 	fmt.Printf("error while writing file %s: %v\n", outPath, err)
	// 	return
	// }
	fmt.Printf("saved file %s (%d bytes)\n", outPath, n)
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
		go handleSession(sess)
	}
}
