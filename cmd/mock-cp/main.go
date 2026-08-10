package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/example/app-runner/internal/mockcp"
)

func main() {
	cp, err := mockcp.NewServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := cp.Start(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer cp.Close()

	cp.AddEnrollmentCode("dev-code")
	fmt.Println("mock control plane running")
	fmt.Println("  enroll url:", cp.EnrollURL())
	fmt.Println("  stream url:", cp.WSURL())
	fmt.Println("  dev enrollment code: dev-code")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
