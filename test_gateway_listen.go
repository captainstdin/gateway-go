package main

import (
	"fmt"
	"net"
)

func main() {
	_, err := net.Listen("tcp", "8.8.8.8:8080")
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println("Success")
	}
}
