package main

import (
	"flag"
	"github.com/captainstdin/gateway-go/pkg/register"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// parseRegisterAddr 解析 Register 监听地址
// 支持格式: "text://0.0.0.0:51234", "tcp://0.0.0.0:51234", "0.0.0.0:51234"
func parseRegisterAddr(addr string) string {
	for _, prefix := range []string{"text://", "tcp://"} {
		if strings.HasPrefix(addr, prefix) {
			return strings.TrimPrefix(addr, prefix)
		}
	}
	return addr
}

func main() {
	listenAddr := flag.String("listen", "text://0.0.0.0:51234", "Register listen address. Format: text://ip:port or tcp://ip:port")
	secretKey := flag.String("key", "", "Secret key for authentication and AES encryption")
	flag.Parse()

	addr := parseRegisterAddr(*listenAddr)
	r := register.New(addr, *secretKey)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("? [Register] Shutting down...")
		r.Stop()
		os.Exit(0)
	}()

	if err := r.Run(); err != nil {
		log.Fatalf("x [Register] Fatal: %v", err)
	}
}
