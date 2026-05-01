package main

import (
	"flag"
	"fmt"
	"gatewayworker-go/pkg/gateway_api"
	"gatewayworker-go/pkg/worker"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	name := flag.String("name", "worker", "Worker name")
	id := flag.Int("id", 0, "Worker ID")
	registerAddr := flag.String("register", "127.0.0.1:51234", "Register address(es), comma separated")
	secretKey := flag.String("key", "", "Secret key")
	flag.Parse()

	bw := worker.New(*name, *id, strings.Split(*registerAddr, ","), *secretKey)

	// Set business callbacks
	bw.OnWorkerStart = func() {
		log.Println("[Worker] Started")
	}
	bw.OnConnect = func(clientID string) {
		log.Printf("[Worker] Client connected: %s", clientID)
	}
	bw.OnMessage = func(clientID string, message []byte) {
		log.Printf("[Worker] Message from %s: %s", clientID, string(message))
		// Echo back
		gateway_api.SendToClient(clientID, []byte(fmt.Sprintf("echo: %s", string(message))))
	}
	bw.OnClose = func(clientID string) {
		log.Printf("[Worker] Client disconnected: %s", clientID)
	}
	bw.OnWebSocketConnect = func(clientID string, data []byte) {
		log.Printf("[Worker] WebSocket connected: %s", clientID)
	}

	gateway_api.SetBusinessWorker(bw)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[Worker] Shutting down...")
		bw.Stop()
		os.Exit(0)
	}()

	if err := bw.Run(); err != nil {
		log.Fatalf("[Worker] Fatal: %v", err)
	}
}
