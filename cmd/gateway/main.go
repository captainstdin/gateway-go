package main

import (
	"flag"
	"gatewayworker-go/pkg/gateway"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	listen := flag.String("listen", "websocket://0.0.0.0:7272", "Listen address(es), comma separated. Format: websocket://ip:port or tcp://ip:port or ws://ip:port")
	lanIP := flag.String("lan-ip", "127.0.0.1", "LAN IP for internal communication")
	startPort := flag.Int("start-port", 2000, "Internal communication start port")
	instanceID := flag.Int("id", 0, "Instance ID")
	registerAddr := flag.String("register", "127.0.0.1:1236", "Register address(es), comma separated")
	secretKey := flag.String("key", "", "Secret key")
	pingInterval := flag.Int("ping-interval", 55, "Ping interval in seconds, 0 to disable")
	pingLimit := flag.Int("ping-limit", 0, "Ping not response limit")
	routerMode := flag.String("router", "least_connections", "Router mode: random or least_connections")
	flag.Parse()

	g := gateway.New(&gateway.Config{
		ListenAddrs:          strings.Split(*listen, ","),
		LanIP:                *lanIP,
		StartPort:            *startPort,
		InstanceID:           *instanceID,
		RegisterAddr:         strings.Split(*registerAddr, ","),
		SecretKey:            *secretKey,
		PingInterval:         *pingInterval,
		PingNotResponseLimit: *pingLimit,
		RouterMode:           gateway.RouterMode(*routerMode),
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[Gateway] Shutting down...")
		g.Stop()
		os.Exit(0)
	}()

	if err := g.Run(); err != nil {
		log.Fatalf("[Gateway] Fatal: %v", err)
	}
}
