package assistant

import (
	"context"
	"os"
	"testing"
)

func TestWeatherService(t *testing.T) {
	// Skip if no API key is available
	apiKey := os.Getenv("WEATHER_API_KEY")
	if apiKey == "" {
		t.Skip("WEATHER_API_KEY not set, skipping weather service tests")
	}

	service := NewWeatherService(apiKey)
	ctx := context.Background()

	t.Run("GetCurrentWeather", func(t *testing.T) {
		weather, err := service.GetCurrentWeather(ctx, "London")
		if err != nil {
			t.Fatalf("Failed to get current weather: %v", err)
		}

		if weather == "" {
			t.Error("Weather response is empty")
		}

		// Check that the response contains expected information
		if !contains(weather, "London") {
			t.Error("Weather response should contain location name")
		}

		if !contains(weather, "°C") {
			t.Error("Weather response should contain temperature in Celsius")
		}

		// Check for new formatting (no emojis, clean structure)
		if !contains(weather, "**Current Weather Conditions:**") {
			t.Error("Weather response should contain formatted section header")
		}

		if !contains(weather, "**Temperature:**") {
			t.Error("Weather response should contain formatted temperature label")
		}
	})

	t.Run("GetForecast", func(t *testing.T) {
		forecast, err := service.GetForecast(ctx, "Paris", 3)
		if err != nil {
			t.Fatalf("Failed to get forecast: %v", err)
		}

		if forecast == "" {
			t.Error("Forecast response is empty")
		}

		// Check that the response contains expected information
		if !contains(forecast, "Paris") {
			t.Error("Forecast response should contain location name")
		}

		if !contains(forecast, "Forecast") {
			t.Error("Forecast response should contain forecast information")
		}

		// Check for new formatting (no emojis, clean structure)
		if !contains(forecast, "**3-Day Weather Forecast:**") {
			t.Error("Forecast response should contain formatted section header")
		}

		if !contains(forecast, "**Today**") {
			t.Error("Forecast response should contain formatted day header")
		}

		if !contains(forecast, "**High:**") {
			t.Error("Forecast response should contain formatted high temperature label")
		}
	})

	t.Run("InvalidLocation", func(t *testing.T) {
		_, err := service.GetCurrentWeather(ctx, "InvalidLocation12345")
		if err == nil {
			t.Error("Expected error for invalid location")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr))
}
