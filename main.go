package main

import (
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	bufferSize    = 32 * 1024        // 32KB buffer, optimal for tunnel traffic
	dialTimeout   = 10 * time.Second
	idleTimeout   = 300 * time.Second // 5 min idle timeout to save Cloud Run CPU billing
)

// Buffer pool to avoid GC pressure on high throughput
var bufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, bufferSize)
		return &buf
	},
}

func transfer(dst net.Conn, src net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := bufPool.Get().(*[]byte)
	defer bufPool.Put(buf)
	io.CopyBuffer(dst, src, *buf)
	// Signal the other side to close
	if conn, ok := dst.(*net.TCPConn); ok {
		conn.CloseWrite()
	}
}

func handleClient(clientConn net.Conn, targetAddr string) {
	defer clientConn.Close()

	// Set deadline on client connection
	clientConn.SetDeadline(time.Now().Add(idleTimeout))

	remoteConn, err := net.DialTimeout("tcp", targetAddr, dialTimeout)
	if err != nil {
		log.Printf("Failed to connect to target %s: %v", targetAddr, err)
		return
	}
	defer remoteConn.Close()

	// Set TCP options for better throughput
	if tc, ok := clientConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(60 * time.Second)
	}
	if tc, ok := remoteConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(60 * time.Second)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go transfer(remoteConn, clientConn, &wg)
	go transfer(clientConn, remoteConn, &wg)
	wg.Wait()
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	targetIP := os.Getenv("V2RAY_SERVER_IP")
	if targetIP == "" {
		log.Fatal("V2RAY_SERVER_IP environment variable is required")
	}

	targetPort := os.Getenv("V2RAY_SERVER_PORT")
	if targetPort == "" {
		targetPort = "80"
	}
	// Validate port
	if _, err := strconv.Atoi(targetPort); err != nil {
		log.Fatalf("Invalid V2RAY_SERVER_PORT: %s", targetPort)
	}

	targetAddr := targetIP + ":" + targetPort
	listenAddr := ":" + port

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}
	defer listener.Close()

	log.Printf("Proxy listening on %s, forwarding to %s", listenAddr, targetAddr)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go handleClient(clientConn, targetAddr)
	}
}