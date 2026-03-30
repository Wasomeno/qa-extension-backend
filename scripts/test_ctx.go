package main

import (
	"context"
	"log"
	"qa-extension-backend/agent"
	"qa-extension-backend/internal/models"
)

func main() {
    log.Println("Starting...")
    
    // Setup playwright once
    err := agent.InitPlaywright()
    if err != nil {
        log.Fatalf("InitPlaywright failed: %v", err)
    }
    defer agent.StopPlaywright()

    // Create a dummy recording
    rec := &models.TestRecording{
        ID: "test-rec-1",
        Name: "Test 1",
        Steps: []models.RecordingStep{
            {
                Action: "navigate",
                Value: "https://example.com",
            },
        },
    }

    // Try a cancelled context
    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    // Run with cancelled context
    log.Println("Running test with cancelled context...")
    _, err = agent.RunTest(ctx, rec)
    if err != nil {
        log.Printf("RunTest failed as expected: %v", err)
    }

    log.Println("Success")
}
