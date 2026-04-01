package main

import (
	"log"
	"mqConnector/Data"
	"mqConnector/routes"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	app := Data.StartDB()

	cancelFunc := routes.InitRoutes(app)

	// Graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, shutting down gracefully...", sig)
		cancelFunc()
	}()

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
