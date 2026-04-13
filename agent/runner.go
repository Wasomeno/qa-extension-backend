package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"time"

	"github.com/playwright-community/playwright-go"
)

var (
	globalPw      *playwright.Playwright
	globalBrowser playwright.Browser
)

func InitPlaywright() error {
	log.Printf("[DEBUG] Starting InitPlaywright")
	log.Printf("[DEBUG] PLAYWRIGHT_NODEJS_PATH: %s", os.Getenv("PLAYWRIGHT_NODEJS_PATH"))
	log.Printf("[DEBUG] PLAYWRIGHT_BROWSERS_PATH: %s", os.Getenv("PLAYWRIGHT_BROWSERS_PATH"))
	log.Printf("[DEBUG] HOME: %s", os.Getenv("HOME"))

	// Check for driver cache
	cachePath := "/root/.cache/ms-playwright-go"
	if _, err := os.Stat(cachePath); err == nil {
		log.Printf("[DEBUG] Driver cache exists at %s", cachePath)
	} else {
		log.Printf("[DEBUG] Driver cache MISSING at %s: %v", cachePath, err)
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Printf("[FATAL ERROR] playwright.Run() failed: %v", err)
		return fmt.Errorf("could not start playwright: %w", err)
	}
	globalPw = pw

	browser, err := globalPw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		SlowMo:   playwright.Float(250),
	})
	if err != nil {
		log.Printf("[FATAL ERROR] globalPw.Chromium.Launch() failed: %v", err)
		return fmt.Errorf("could not launch browser: %w", err)
	}
	globalBrowser = browser
	return nil
}

func StopPlaywright() {
	if globalBrowser != nil {
		globalBrowser.Close()
	}
	if globalPw != nil {
		globalPw.Stop()
	}
}

func RunTest(ctx context.Context, recording *models.TestRecording) (*models.TestResult, error) {
	log.Printf("[Runner] Running test: %s", recording.Name)

	// Helper to publish execution events to the unified stream
	publish := func(stage, msg string, stepInfo *database.StreamStepInfo) {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:         "execution",
			ResourceType: "recording",
			ResourceID:   recording.ID,
			Stage:        stage,
			Message:      msg,
			StepInfo:     stepInfo,
		})
	}

	publish("start", fmt.Sprintf("Starting test '%s' (%d steps)...", recording.Name, len(recording.Steps)), nil)

	if globalBrowser == nil {
		publish("step", "Initializing Playwright browser...", &database.StreamStepInfo{CurrentStep: 0, TotalSteps: len(recording.Steps), StepName: "Initializing browser", Action: ""})
		if err := InitPlaywright(); err != nil {
			publish("error", fmt.Sprintf("Failed to initialize Playwright: %v", err), nil)
			return nil, err
		}
	}

	// Create a temporary directory for the video
	videoDir, err := os.MkdirTemp("", "test-video-*")
	if err != nil {
		return nil, fmt.Errorf("could not create temp dir: %w", err)
	}
	defer os.RemoveAll(videoDir)

	pwCtx, err := globalBrowser.NewContext(playwright.BrowserNewContextOptions{
		RecordVideo: &playwright.RecordVideo{
			Dir: videoDir,
			Size: &playwright.Size{
				Width:  1920,
				Height: 1080,
			},
		},
		Viewport: &playwright.Size{
			Width:  1920,
			Height: 1080,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("could not create context: %w", err)
	}
	defer pwCtx.Close()

	page, err := pwCtx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("could not create page: %w", err)
	}
	defer page.Close()

	result := &models.TestResult{
		TestID:      recording.ID,
		Status:      "passed",
		StepResults: make([]models.TestStepResult, 0),
	}

	totalSteps := len(recording.Steps)
	for i, step := range recording.Steps {
		// Create a per-step timeout context (60 seconds)
		stepCtx, stepCancel := context.WithTimeout(ctx, 60*time.Second)

		select {
		case <-ctx.Done():
			stepCancel()
			result.Status = "timeout"
			result.Log = "Test execution timed out during step execution"
			publish("error", "Test timed out during step execution", nil)
			return result, nil
		default:
		}

		currentStep := i + 1
		stepInfo := &database.StreamStepInfo{
			CurrentStep: currentStep,
			TotalSteps:  totalSteps,
			StepName:    step.Description,
			Action:      step.Action,
		}
		publish("step", fmt.Sprintf("Running '%s' — Step %d/%d: %s...", recording.Name, currentStep, totalSteps, step.Description), stepInfo)
		log.Printf("[Runner] Step %d: %s (%s)", currentStep, step.Description, step.Action)

		stepResult := models.TestStepResult{
			StepIndex: i,
			Status:    "success",
		}

		// Execute the step using a separate goroutine to handle per-step timeout
		errChan := make(chan error, 1)
		go func() {
			errChan <- executeStep(page, step)
		}()

		var err error
		select {
		case err = <-errChan:
			// Step finished within timeout
		case <-stepCtx.Done():
			err = stepCtx.Err()
		}
		stepCancel()

		if err != nil {
			log.Printf("[Runner] Step %d failed: %v", i+1, err)
			stepResult.Status = "failure"
			stepResult.Error = err.Error()

			if stepCtx.Err() == context.DeadlineExceeded {
				stepResult.Error = "Step timed out after 60 seconds"
				result.Status = "timeout"
			} else {
				result.Status = "failed"
			}

			// Take screenshot on failure
			screenshot, _ := page.Screenshot()
			if screenshot != nil {
				stepResult.Screenshot = base64.StdEncoding.EncodeToString(screenshot)
			}

			result.StepResults = append(result.StepResults, stepResult)

			// If it's a timeout or serious error, stop immediately
			if result.Status == "timeout" || result.Status == "failed" {
				publish("error", fmt.Sprintf("Test failed at Step %d/%d: %s", currentStep, totalSteps, step.Description), stepInfo)
				return result, nil
			}
			break
		}

		// Optionally take screenshot on success for specific actions like navigate
		if step.Action == "navigate" {
			screenshot, _ := page.Screenshot()
			if screenshot != nil {
				stepResult.Screenshot = base64.StdEncoding.EncodeToString(screenshot)
			}
		}

		result.StepResults = append(result.StepResults, stepResult)
	}

	// Wait a moment at the end to ensure the last action is captured in the video
	select {
	case <-ctx.Done():
		result.Status = "timeout"
		result.Log = "Test execution timed out after final step"
		publish("error", "Test timed out after final step", nil)
		return result, nil
	case <-time.After(200 * time.Millisecond):
	}

	// Get video object before closing
	video := page.Video()

	// Close page and context to ensure video is written
	page.Close()
	pwCtx.Close()

	// Give a moment for the video to be finalized
	select {
	case <-ctx.Done():
		result.Status = "timeout"
		result.Log = "Test execution timed out during video finalization"
		publish("error", "Test timed out during video finalization", nil)
		return result, nil
	case <-time.After(300 * time.Millisecond):
	}

	// Upload video to R2 if available
	publish("step", fmt.Sprintf("Test '%s' completed, uploading video...", recording.Name), &database.StreamStepInfo{CurrentStep: totalSteps, TotalSteps: totalSteps, StepName: "Uploading video", Action: ""})
	if video != nil {
		path, err := video.Path()
		if err == nil {
			log.Printf("[Runner] Video path on disk: %s", path)
			r2, r2Err := client.NewR2Client()
			if r2Err == nil {
				// Use the actual file name from disk (finalized by Playwright) as the key
				// to avoid any ID/UUID mismatches
				fileName := filepath.Base(path)
				key := fmt.Sprintf("videos/%s", fileName)
				log.Printf("[Runner] Uploading video with key: %s", key)
				videoURL, uploadErr := r2.UploadFile(ctx, path, key, "video/webm")
				if uploadErr == nil {
					result.VideoURL = videoURL
					log.Printf("[Runner] Video uploaded to: %s", videoURL)
				} else {
					log.Printf("[Runner] Failed to upload video: %v", uploadErr)
				}
			} else {
				log.Printf("[Runner] R2 client not configured, skipping video upload: %v", r2Err)
			}
		}
	}

	publish("done", fmt.Sprintf("Test '%s' completed: %s", recording.Name, result.Status), &database.StreamStepInfo{CurrentStep: totalSteps, TotalSteps: totalSteps, StepName: "Completed", Action: ""})
	return result, nil
}

func RunTestsParallel(ctx context.Context, recordings []models.TestRecording) []*models.TestResult {
	log.Printf("[Runner] Running %d tests in parallel", len(recordings))

	// Helper to publish execution events to the unified stream
	publish := func(stage, msg string, stepInfo *database.StreamStepInfo) {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:         "execution",
			ResourceType: "recording",
			ResourceID:   "", // Multiple recordings, no single resource ID
			Stage:        stage,
			Message:      msg,
			StepInfo:     stepInfo,
		})
	}

	total := len(recordings)
	publish("start", fmt.Sprintf("Starting parallel execution of %d tests...", total), &database.StreamStepInfo{TotalSteps: total, StepName: "Starting parallel execution"})

	results := make([]*models.TestResult, len(recordings))

	// Use a worker pool with a concurrency limit of 3
	semaphore := make(chan struct{}, 3)

	for i := range recordings {
		semaphore <- struct{}{}
		go func(idx int) {
			defer func() {
				<-semaphore
			}()

			rec := recordings[idx]
			publish("step", fmt.Sprintf("Starting test '%s' (%d/%d)...", rec.Name, idx+1, total), &database.StreamStepInfo{CurrentStep: idx + 1, TotalSteps: total, StepName: rec.Name, Action: ""})
			result, err := RunTest(ctx, &rec)
			if err != nil {
				results[idx] = &models.TestResult{
					TestID: rec.ID,
					Status: "failed",
					Log:    err.Error(),
				}
				publish("step", fmt.Sprintf("Test '%s' failed: %v", rec.Name, err), &database.StreamStepInfo{CurrentStep: idx + 1, TotalSteps: total, StepName: rec.Name, Action: ""})
			} else {
				results[idx] = result
				statusMsg := result.Status
				publish("step", fmt.Sprintf("Test '%s' completed: %s", rec.Name, statusMsg), &database.StreamStepInfo{CurrentStep: idx + 1, TotalSteps: total, StepName: rec.Name, Action: ""})
			}
		}(i)
	}

	// Wait for all workers to finish by filling the semaphore
	for i := 0; i < 3; i++ {
		semaphore <- struct{}{}
	}

	// Summarize results
	passed := 0
	failed := 0
	for _, r := range results {
		if r.Status == "passed" {
			passed++
		} else {
			failed++
		}
	}

	publish("done", fmt.Sprintf("Parallel execution complete: %d passed, %d failed out of %d total", passed, failed, total), &database.StreamStepInfo{TotalSteps: total, StepName: "Parallel execution complete"})

	return results
}

func executeStep(page playwright.Page, step models.RecordingStep) error {
	log.Printf("[Runner] Executing action: %s on selector: %s with value: %s", step.Action, step.Selector, step.Value)

	// Helper function to try xpath-based selectors first, then fall back to CSS selectors
	tryWithFallback := func(playwrightFunc func(selector string) error) error {
		var lastErr error
		success := false

		// 1. Try XPath if available (primary selector)
		if step.XPath != "" {
			log.Printf("[Runner] Trying XPath: %s", step.XPath)
			if err := playwrightFunc(step.XPath); err == nil {
				success = true
				log.Printf("[Runner] XPath succeeded: %s", step.XPath)
			} else {
				lastErr = err
				log.Printf("[Runner] XPath failed: %v", err)
			}
		}

		// 2. Try XPathCandidates (fallback xpath options)
		if !success {
			for i, xpath := range step.XPathCandidates {
				log.Printf("[Runner] Trying XPathCandidate[%d]: %s", i, xpath)
				if err := playwrightFunc(xpath); err == nil {
					success = true
					log.Printf("[Runner] XPathCandidate[%d] succeeded: %s", i, xpath)
					break
				} else {
					lastErr = err
					log.Printf("[Runner] XPathCandidate[%d] failed: %v", i, err)
				}
			}
		}

		// 3. Try CSS Selector (fallback to CSS)
		if !success && step.Selector != "" {
			log.Printf("[Runner] Trying CSS Selector: %s", step.Selector)
			if err := playwrightFunc(step.Selector); err == nil {
				success = true
				log.Printf("[Runner] CSS Selector succeeded: %s", step.Selector)
			} else {
				lastErr = err
				log.Printf("[Runner] CSS Selector failed: %v", err)
			}
		}

		// 4. Try SelectorCandidates (fallback CSS options)
		if !success {
			for i, candidate := range step.SelectorCandidates {
				log.Printf("[Runner] Trying SelectorCandidate[%d]: %s", i, candidate)
				if err := playwrightFunc(candidate); err == nil {
					success = true
					log.Printf("[Runner] SelectorCandidate[%d] succeeded: %s", i, candidate)
					break
				} else {
					lastErr = err
					log.Printf("[Runner] SelectorCandidate[%d] failed: %v", i, err)
				}
			}
		}

		if !success {
			return fmt.Errorf("all selectors failed (tried xpath, xpathCandidates, css, cssCandidates): %w", lastErr)
		}
		return nil
	}

	switch step.Action {
	case "navigate":
		url := step.Value
		if url == "" && step.Selector != "" {
			url = step.Selector
		}
		if _, err := page.Goto(url); err != nil {
			return fmt.Errorf("navigate failed: %w", err)
		}
	case "type":
		typeOptions := playwright.PageTypeOptions{
			Delay: playwright.Float(100),
		}
		err := tryWithFallback(func(selector string) error {
			return page.Type(selector, step.Value, typeOptions)
		})
		if err != nil {
			return err
		}
	case "click":
		clickOptions := playwright.PageClickOptions{
			Delay: playwright.Float(150),
		}
		err := tryWithFallback(func(selector string) error {
			return page.Click(selector, clickOptions)
		})
		if err != nil {
			return err
		}
	case "press":
		err := tryWithFallback(func(selector string) error {
			return page.Press(selector, step.Value)
		})
		if err != nil {
			return err
		}
	case "wait":
		// wait for selector - prioritize xpath
		if step.XPath != "" {
			_, err := page.WaitForSelector(step.XPath)
			if err == nil {
				return nil
			}
		}
		for _, xpath := range step.XPathCandidates {
			_, err := page.WaitForSelector(xpath)
			if err == nil {
				return nil
			}
		}
		if step.Selector != "" {
			_, err := page.WaitForSelector(step.Selector)
			return err
		}
		return nil
	case "assert":
		// very basic assertion - prioritize xpath
		if step.AssertionType == "visible" {
			// Try xpath first
			if step.XPath != "" {
				_, err := page.WaitForSelector(step.XPath, playwright.PageWaitForSelectorOptions{
					State: playwright.WaitForSelectorStateVisible,
				})
				if err == nil {
					return nil
				}
			}
			// Try xpath candidates
			for _, xpath := range step.XPathCandidates {
				_, err := page.WaitForSelector(xpath, playwright.PageWaitForSelectorOptions{
					State: playwright.WaitForSelectorStateVisible,
				})
				if err == nil {
					return nil
				}
			}
			// Fall back to CSS selector
			if step.Selector != "" {
				_, err := page.WaitForSelector(step.Selector, playwright.PageWaitForSelectorOptions{
					State: playwright.WaitForSelectorStateVisible,
				})
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
	return nil
}
