package main

import (
	"fmt"
	"os"

	"github.com/makecindy/cindy-server/internal/platform/authn"
)

func main() {
	path := "./.keys/jwt-ed25519.pem"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if _, err := authn.NewManager(path, "cindy", 0); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("jwt key ready:", path)
}
