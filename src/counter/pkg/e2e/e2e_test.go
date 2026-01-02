package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

const (
	writeAPIURL = "http://localhost:8080"
	readAPIURL  = "http://localhost:8081"
)

type EventRequest struct {
	Id         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	EventKey   string    `json:"eventkey"`
	EventValue uint32    `json:"eventvalue"`
}

type CounterResponse struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

func TestCounterE2E(t *testing.T) {
	// Test case 1: increment a counter and check the value.
	t.Run("Increment counter", func(t *testing.T) {
		eventKey := "test-key-1"
		eventValue := uint32(10)

		// 1. Send a write event to the write API.
		if err := sendEvent(eventKey, eventValue); err != nil {
			t.Fatalf("failed to send event: %v", err)
		}

		// 2. Poll the read API until the counter value is updated.
		finalValue, err := pollCounter(eventKey, int64(eventValue))
		if err != nil {
			t.Fatalf("failed to poll counter: %v", err)
		}

		// 3. Assert that the final counter value is correct.
		if finalValue != int64(eventValue) {
			t.Fatalf("expected counter value %d, got %d", eventValue, finalValue)
		}
	})

	// Test case 2: increment another counter and check the value.
	t.Run("Increment another counter", func(t *testing.T) {
		eventKey := "test-key-2"
		eventValue := uint32(20)

		// 1. Send a write event to the write API.
		if err := sendEvent(eventKey, eventValue); err != nil {
			t.Fatalf("failed to send event: %v", err)
		}

		// 2. Poll the read API until the counter value is updated.
		finalValue, err := pollCounter(eventKey, int64(eventValue))
		if err != nil {
			t.Fatalf("failed to poll counter: %v", err)
		}

		// 3. Assert that the final counter value is correct.
		if finalValue != int64(eventValue) {
			t.Fatalf("expected counter value %d, got %d", eventValue, finalValue)
		}
	})
}

func sendEvent(eventKey string, eventValue uint32) error {
	event := EventRequest{
		Id:         "test-id",
		Timestamp:  time.Now(),
		EventKey:   eventKey,
		EventValue: eventValue,
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	resp, err := http.Post(fmt.Sprintf("%s/event", writeAPIURL), "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to send event to write API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("expected status code %d, got %d", http.StatusAccepted, resp.StatusCode)
	}

	return nil
}

func pollCounter(eventKey string, expectedValue int64) (int64, error) {
	var counterValue int64
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		resp, err := http.Get(fmt.Sprintf("%s/counter?key=%s", readAPIURL, eventKey))
		if err != nil {
			return 0, fmt.Errorf("failed to get counter value from read API: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			// continue polling if the key is not found
			if resp.StatusCode == http.StatusInternalServerError {
				continue
			}
			return 0, fmt.Errorf("expected status code %d, got %d", http.StatusOK, resp.StatusCode)
		}

		var counterResp CounterResponse
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return 0, fmt.Errorf("failed to read response body: %w", err)
		}

		if err := json.Unmarshal(body, &counterResp); err != nil {
			return 0, fmt.Errorf("failed to unmarshal counter response: %w", err)
		}

		counterValue = counterResp.Value
		if counterValue >= expectedValue {
			break
		}
	}
	return counterValue, nil
}
