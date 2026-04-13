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

	// Helper: Wait for page to settle after navigation (React/Angular apps need time)
	waitForPageSettled := func() error {
		// Wait for DOM to be ready
		if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateLoad,
		}); err != nil {
			log.Printf("[Runner] Load wait warning: %v", err)
		}

		// Wait for React/Angular hydration and initial render
		page.WaitForTimeout(2000)

		// Try to wait for network idle (capped at reasonable time)
		// This handles apps that fetch data on load
		for i := 0; i < 2; i++ {
			if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
				State:   playwright.LoadStateNetworkidle,
				Timeout: playwright.Float(5000), // 5 second timeout
			}); err == nil {
				log.Printf("[Runner] NetworkIdle reached")
				break
			}
			log.Printf("[Runner] NetworkIdle not reached, waiting...")
			page.WaitForTimeout(1000)
		}

		// Additional wait after networkidle for React to finish rendering
		// Critical: React apps often finish network requests but still have pending renders
		page.WaitForTimeout(2000)
		return nil
	}

	// Helper: Resolve element with polling - waits for element to appear and be actionable
	// Returns the raw selector string (not a Locator) to avoid strict mode issues
	resolveElement := func(timeout time.Duration) (string, error) {
		allSelectors := []string{}

		// Collect all selectors in priority order: XPath first, then CSS
		if step.XPath != "" {
			allSelectors = append(allSelectors, step.XPath)
		}
		allSelectors = append(allSelectors, step.XPathCandidates...)
		if step.Selector != "" {
			allSelectors = append(allSelectors, step.Selector)
		}
		allSelectors = append(allSelectors, step.SelectorCandidates...)

		if len(allSelectors) == 0 {
			return "", fmt.Errorf("no selectors available")
		}

		start := time.Now()
		attempts := 0
		var lastErr error

		for time.Since(start) < timeout {
			attempts++

			// Try each selector in order
			for _, selector := range allSelectors {
				if selector == "" {
					continue
				}

				log.Printf("[Runner] Attempting selector (%d): %s", attempts, selector)

				// Check if element exists - use .First() to avoid strict mode issues
				locator := page.Locator(selector)
				count, err := locator.Count()

				if err != nil {
					lastErr = err
					log.Printf("[Runner] Selector '%s' error: %v", selector, err)
					continue
				}

				if count == 0 {
					log.Printf("[Runner] Selector '%s' found 0 elements", selector)
					continue
				}

				if count > 1 {
					log.Printf("[Runner] Selector '%s' found %d elements, will use first visible one", selector, count)
				}

				// Check if FIRST matching element is visible/actionable
				isVisible, err := locator.First().IsVisible()
				if err != nil {
					lastErr = err
					continue
				}

				if !isVisible {
					log.Printf("[Runner] Selector '%s' first element not visible, checking others...", selector)
					// Try to find ANY visible element with this selector
					for i := 0; i < int(count) && i < 10; i++ {
						elem := locator.Nth(i)
						if vis, err := elem.IsVisible(); err == nil && vis {
							log.Printf("[Runner] Selector '%s' found visible element at index %d", selector, i)
							log.Printf("[Runner] Successfully resolved element with selector: %s", selector)
							return selector, nil
						}
					}
					log.Printf("[Runner] Selector '%s' found elements but none visible, waiting...", selector)
					continue
				}

				// Element is visible! Return the selector string
				log.Printf("[Runner] Successfully resolved element with selector: %s", selector)
				return selector, nil
			}

			// No selector worked this round, wait before retrying
			// Use exponential backoff: wait longer if element still not found
			waitTimeMs := 500
			if attempts > 3 {
				waitTimeMs = 1000
			}
			if attempts > 5 {
				waitTimeMs = 1500
			}
			log.Printf("[Runner] Waiting %dms before retry (attempt %d)...", waitTimeMs, attempts+1)
			page.WaitForTimeout(float64(waitTimeMs))

			// Periodically wait for DOM to be quiet (helps with dynamic content)
			if attempts%5 == 0 {
				log.Printf("[Runner] Waiting for DOM to settle after %d attempts...", attempts)
				page.WaitForTimeout(1000)
			}
		}

		return "", fmt.Errorf("element not found after %d attempts over %v: %w", attempts, timeout, lastErr)
	}

	switch step.Action {
	case "navigate":
		url := step.Value
		if url == "" && step.Selector != "" {
			url = step.Selector
		}

		log.Printf("[Runner] Navigating to: %s", url)

		// Navigate with proper waiting
		if _, err := page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateLoad,
			Timeout:   playwright.Float(30000),
		}); err != nil {
			return fmt.Errorf("navigate failed: %w", err)
		}

		// CRITICAL: Wait for page to settle after navigation
		log.Printf("[Runner] Waiting for page to settle after navigation...")

		// Use the same settling strategy as other actions
		waitForPageSettled()

	case "type":
		// First, wait for page to settle if this is after a navigation
		waitForPageSettled()

		// Resolve element with polling (30 second timeout)
		usedSelector, err := resolveElement(30 * time.Second)
		if err != nil {
			return fmt.Errorf("type failed: %w", err)
		}

		log.Printf("[Runner] Typing '%s' into resolved element (selector: %s)", step.Value, usedSelector)

		// Use .First() to avoid strict mode violation with multiple matches
		locator := page.Locator(usedSelector).First()

		// Clear existing value first
		locator.Clear()

		// Type with delay for realistic simulation
		delay := float64(100)
		typeOptions := playwright.LocatorTypeOptions{
			Delay: &delay,
		}
		if err := locator.Type(step.Value, typeOptions); err != nil {
			return fmt.Errorf("type action failed: %w", err)
		}

	case "click":
		// First, wait for page to settle
		waitForPageSettled()

		// Resolve element with polling
		usedSelector, err := resolveElement(30 * time.Second)
		if err != nil {
			return fmt.Errorf("click failed: %w", err)
		}

		log.Printf("[Runner] Clicking resolved element (selector: %s)", usedSelector)

		// Use .First() to avoid strict mode violation with multiple matches
		locator := page.Locator(usedSelector).First()

		// Scroll element into view if needed
		if err := locator.ScrollIntoViewIfNeeded(); err != nil {
			log.Printf("[Runner] ScrollIntoViewIfNeeded warning: %v", err)
		}

		// Click with small delay
		delay := float64(150)
		clickOptions := playwright.LocatorClickOptions{
			Delay:  &delay,
			Force:  playwright.Bool(false),
		}
		if err := locator.Click(clickOptions); err != nil {
			return fmt.Errorf("click action failed: %w", err)
		}

	case "press":
		// Wait for page to settle
		waitForPageSettled()

		// Resolve element with polling
		usedSelector, err := resolveElement(30 * time.Second)
		if err != nil {
			return fmt.Errorf("press failed: %w", err)
		}

		log.Printf("[Runner] Pressing '%s' on resolved element (selector: %s)", step.Value, usedSelector)

		// Use .First() to avoid strict mode violation with multiple matches
		locator := page.Locator(usedSelector).First()
		if err := locator.Press(step.Value); err != nil {
			return fmt.Errorf("press action failed: %w", err)
		}

	case "wait":
		// Explicit wait - resolve element with longer timeout
		_, err := resolveElement(60 * time.Second)
		return err

	case "assert":
		// Wait for page to settle
		waitForPageSettled()

		// Resolve element and check visibility
		usedSelector, err := resolveElement(30 * time.Second)
		if err != nil {
			if step.AssertionType == "not_exists" {
				return nil // Expected: element should NOT exist
			}
			return fmt.Errorf("assert failed: %w", err)
		}

		if step.AssertionType == "not_exists" {
			return fmt.Errorf("assert failed: element should not exist but was found with selector: %s", usedSelector)
		}

		// For visible assertions, verify element is actually visible
		// Use .First() to avoid strict mode violation with multiple matches
		locator := page.Locator(usedSelector).First()
		isVisible, _ := locator.IsVisible()
		if !isVisible && step.AssertionType == "visible" {
			return fmt.Errorf("assert failed: element found but not visible")
		}

		log.Printf("[Runner] Assert passed for selector: %s", usedSelector)
		return nil

	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
	return nil
}
