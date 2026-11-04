package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/jurasikasan/units/auth_service/internal/password"
)

func main() {
	in, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && in == "" {
		fmt.Fprintln(os.Stderr, "read password from stdin:", err)
		os.Exit(1)
	}

	plain := strings.TrimRight(in, "\r\n")
	if plain == "" {
		fmt.Fprintln(os.Stderr, "empty password")
		os.Exit(1)
	}

	if err := password.Validate(plain); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	hash, err := password.Hash(plain)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
