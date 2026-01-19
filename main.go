package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	baseURL := os.Getenv("BACKEND_URL")
	if baseURL == "" {
		fmt.Println("BACKEND_URL environment variable is required")
		os.Exit(1)
	}

	authToken := os.Getenv("PIPELINE_API_TOKEN")
	if authToken == "" {
		fmt.Println("PIPELINE_API_TOKEN environment variable is required")
		os.Exit(1)
	}

	url := baseURL + "/v1/internal/pipelines/all"

	client := &http.Client{Timeout: 3 * time.Minute}

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		fmt.Printf("Failed to create request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Failed to call pipeline endpoint: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("Pipeline triggered successfully (status: %d)\n", resp.StatusCode)
	} else {
		fmt.Printf("Pipeline request failed with status: %d\n", resp.StatusCode)
		os.Exit(1)
	}
}
