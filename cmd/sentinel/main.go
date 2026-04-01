package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sentinel <analyze|digest>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "analyze":
		if err := runAnalyze(); err != nil {
			fmt.Fprintf(os.Stderr, "analyze failed: %v\n", err)
			os.Exit(1)
		}
	case "digest":
		if err := runDigest(); err != nil {
			fmt.Fprintf(os.Stderr, "digest failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runAnalyze() error {
	fmt.Println("sentinel: analyze not yet implemented")
	return nil
}

func runDigest() error {
	fmt.Println("sentinel: digest not yet implemented")
	return nil
}
