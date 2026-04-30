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

    // Create a dummy test run
    rec := &models.TestRun{
        ID: "test-rec-1",
        Name: "Test 1",
        Steps: []models.RecordingStep{
            {
                Action: "navigate",
                Value: "https://example.com",
            },
        },
    }

    ctx := context.Background()

    // Run first time
    log.Println("Running test first time...")
    _, err = agent.RunTest(ctx, rec)
    if err != nil {
        log.Fatalf("RunTest 1 failed: %v", err)
    }

    // Run second time
    log.Println("Running test second time...")
    _, err = agent.RunTest(ctx, rec)
    if err != nil {
        log.Fatalf("RunTest 2 failed: %v", err)
    }

    log.Println("Success")
}
