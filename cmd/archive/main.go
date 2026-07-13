package main

import (
	"log"
	"os"

	"archive/internal/app"
)

func main() {
	cfg := app.Config{
		Addr:    env("ARCHIVE_ADDR", ":8080"),
		DataDir: env("ARCHIVE_DATA", "data"),
	}
	if err := app.Run(cfg); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
