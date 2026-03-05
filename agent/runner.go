package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"qa-extension-backend/client"
	"qa-extension-backend/models"
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

	// Get progress channel from context if available
	progressCh, _ := ctx.Value("progressCh").(chan string)
	sendProgress := func(msg string) {
		if progressCh != nil {
			select {
			case progressCh <- msg:
			default:
				// Skip if channel is full
			}
		}
	}

	sendProgress("Initializing Playwright browser...")
	if globalBrowser == nil {
		if err := InitPlaywright(); err != nil {
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

	for i, step := range recording.Steps {
		// Create a per-step timeout context (60 seconds)
		stepCtx, stepCancel := context.WithTimeout(ctx, 60*time.Second)
		
		select {
		case <-ctx.Done():
			stepCancel()
			result.Status = "timeout"
			result.Log = "Test execution timed out during step execution"
			return result, nil
		default:
		}

		sendProgress(fmt.Sprintf("Step %d: %s", i+1, step.Description))
		log.Printf("[Runner] Step %d: %s (%s)", i+1, step.Description, step.Action)
		
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
		return result, nil
	case <-time.After(300 * time.Millisecond):
	}

	// Upload video to R2 if available
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

	return result, nil
}

func executeStep(page playwright.Page, step models.RecordingStep) error {
	log.Printf("[Runner] Executing action: %s on selector: %s with value: %s", step.Action, step.Selector, step.Value)

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
		if err := page.Type(step.Selector, step.Value, typeOptions); err != nil {
			// Try candidates
			success := false
			for _, candidate := range step.SelectorCandidates {
				if err := page.Type(candidate, step.Value, typeOptions); err == nil {
					success = true
					break
				}
			}
			if !success {
				return fmt.Errorf("type failed for all candidates: %w", err)
			}
		}
	case "click":
		clickOptions := playwright.PageClickOptions{
			Delay: playwright.Float(150),
		}
		if err := page.Click(step.Selector, clickOptions); err != nil {
			success := false
			for _, candidate := range step.SelectorCandidates {
				if err := page.Click(candidate, clickOptions); err == nil {
					success = true
					break
				}
			}
			if !success {
				return fmt.Errorf("click failed for all candidates: %w", err)
			}
		}
	case "press":
		return page.Press(step.Selector, step.Value)
	case "wait":
		// wait for selector
		if step.Selector != "" {
			_, err := page.WaitForSelector(step.Selector)
			return err
		}
		return nil
	case "assert":
		// very basic assertion
		if step.AssertionType == "visible" {
			_, err := page.WaitForSelector(step.Selector, playwright.PageWaitForSelectorOptions{
				State: playwright.WaitForSelectorStateVisible,
			})
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
	return nil
}
