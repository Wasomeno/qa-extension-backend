package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Init loads the .env file if present. It does not panic if the file is missing
// as environment variables might be provided directly (e.g., via Docker).
func Init() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error loading it; relying on system environment variables.")
	}
}

// GetEnv retrieves the value of the environment variable named by the key.
// It returns the value, which will be empty if the variable is not present.
// If the variable is not found and required is true, it panics.
func GetEnv(key string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		log.Fatalf("CRITICAL: required environment variable %s is missing.", key)
	}
	return value
}

// GetEnvOrDefault retrieves the value of the environment variable named by the key.
// It returns the default value if the variable is not present.
func GetEnvOrDefault(key, defaultValue string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return defaultValue
	}
	return value
}
