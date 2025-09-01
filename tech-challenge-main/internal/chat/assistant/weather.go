package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type WeatherService struct {
	apiKey  string
	client  *http.Client
	baseURL string
}

type WeatherResponse struct {
	Location struct {
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		Region    string  `json:"region"`
		Lat       float64 `json:"lat"`
		Lon       float64 `json:"lon"`
		Localtime string  `json:"localtime"`
	} `json:"location"`
	Current struct {
		TempC     float64 `json:"temp_c"`
		TempF     float64 `json:"temp_f"`
		Condition struct {
			Text string `json:"text"`
			Icon string `json:"icon"`
		} `json:"condition"`
		WindKph      float64 `json:"wind_kph"`
		WindMph      float64 `json:"wind_mph"`
		WindDegree   int     `json:"wind_degree"`
		WindDir      string  `json:"wind_dir"`
		Humidity     int     `json:"humidity"`
		FeelsLikeC   float64 `json:"feelslike_c"`
		FeelsLikeF   float64 `json:"feelslike_f"`
		UV           float64 `json:"uv"`
		VisibilityKm float64 `json:"vis_km"`
	} `json:"current"`
	Forecast struct {
		Forecastday []struct {
			Date string `json:"date"`
			Day  struct {
				MaxtempC      float64 `json:"maxtemp_c"`
				MaxtempF      float64 `json:"maxtemp_f"`
				MintempC      float64 `json:"mintemp_c"`
				MintempF      float64 `json:"mintemp_f"`
				AvgtempC      float64 `json:"avgtemp_c"`
				AvgtempF      float64 `json:"avgtemp_f"`
				MaxwindKph    float64 `json:"maxwind_kph"`
				MaxwindMph    float64 `json:"maxwind_mph"`
				TotalprecipMm float64 `json:"totalprecip_mm"`
				TotalprecipIn float64 `json:"totalprecip_in"`
				Condition     struct {
					Text string `json:"text"`
					Icon string `json:"icon"`
				} `json:"condition"`
			} `json:"day"`
			Hour []struct {
				TimeEpoch int64   `json:"time_epoch"`
				Time      string  `json:"time"`
				TempC     float64 `json:"temp_c"`
				TempF     float64 `json:"temp_f"`
				Condition struct {
					Text string `json:"text"`
					Icon string `json:"icon"`
				} `json:"condition"`
				WindKph      float64 `json:"wind_kph"`
				WindMph      float64 `json:"wind_mph"`
				WindDegree   int     `json:"wind_degree"`
				WindDir      string  `json:"wind_dir"`
				Humidity     int     `json:"humidity"`
				ChanceOfRain int     `json:"chance_of_rain"`
			} `json:"hour"`
		} `json:"forecastday"`
	} `json:"forecast"`
}

type WeatherError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func NewWeatherService(apiKey string) *WeatherService {
	return &WeatherService{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: "http://api.weatherapi.com/v1",
	}
}

func (w *WeatherService) GetCurrentWeather(ctx context.Context, location string) (string, error) {
	params := url.Values{}
	params.Set("key", w.apiKey)
	params.Set("q", location)
	params.Set("aqi", "no")

	req, err := http.NewRequestWithContext(ctx, "GET", w.baseURL+"/current.json?"+params.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var weatherErr WeatherError
		if err := json.Unmarshal(body, &weatherErr); err == nil && weatherErr.Error.Message != "" {
			return "", fmt.Errorf("weather API error: %s", weatherErr.Error.Message)
		}
		return "", fmt.Errorf("weather API returned status %d: %s", resp.StatusCode, string(body))
	}

	var weather WeatherResponse
	if err := json.Unmarshal(body, &weather); err != nil {
		return "", fmt.Errorf("failed to parse weather response: %w", err)
	}

	return w.formatCurrentWeather(weather), nil
}

func (w *WeatherService) GetForecast(ctx context.Context, location string, days int) (string, error) {
	if days < 1 || days > 14 {
		days = 3 // Default to 3 days
	}

	params := url.Values{}
	params.Set("key", w.apiKey)
	params.Set("q", location)
	params.Set("days", strconv.Itoa(days))
	params.Set("aqi", "no")
	params.Set("alerts", "no")

	req, err := http.NewRequestWithContext(ctx, "GET", w.baseURL+"/forecast.json?"+params.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var weatherErr WeatherError
		if err := json.Unmarshal(body, &weatherErr); err == nil && weatherErr.Error.Message != "" {
			return "", fmt.Errorf("weather API error: %s", weatherErr.Error.Message)
		}
		return "", fmt.Errorf("weather API returned status %d: %s", resp.StatusCode, string(body))
	}

	var weather WeatherResponse
	if err := json.Unmarshal(body, &weather); err != nil {
		return "", fmt.Errorf("failed to parse weather response: %w", err)
	}

	return w.formatForecast(weather), nil
}

// formatCurrentWeather formats current weather data into a beautiful, readable response
func (w *WeatherService) formatCurrentWeather(weather WeatherResponse) string {
	loc := weather.Location
	current := weather.Current

	var sb strings.Builder

	// Header with location and time
	sb.WriteString(fmt.Sprintf("**%s, %s**\n", loc.Name, loc.Country))
	sb.WriteString(fmt.Sprintf("Coordinates: %.2f, %.2f\n", loc.Lat, loc.Lon))
	sb.WriteString(fmt.Sprintf("Local Time: %s\n\n", loc.Localtime))

	// Current weather section
	sb.WriteString("**Current Weather Conditions:**\n")
	sb.WriteString(fmt.Sprintf("**Temperature:** %.1f°C (%.1f°F)\n", current.TempC, current.TempF))
	sb.WriteString(fmt.Sprintf("**Conditions:** %s\n", current.Condition.Text))
	sb.WriteString(fmt.Sprintf("**Wind:** %.1f km/h (%.1f mph) %s\n", current.WindKph, current.WindMph, current.WindDir))
	sb.WriteString(fmt.Sprintf("**Humidity:** %d%%\n", current.Humidity))
	sb.WriteString(fmt.Sprintf("**Feels Like:** %.1f°C (%.1f°F)\n", current.FeelsLikeC, current.FeelsLikeF))
	sb.WriteString(fmt.Sprintf("**UV Index:** %.1f\n", current.UV))
	sb.WriteString(fmt.Sprintf("**Visibility:** %.1f km\n", current.VisibilityKm))

	return sb.String()
}

// formatForecast formats forecast data into a beautiful, readable response
func (w *WeatherService) formatForecast(weather WeatherResponse) string {
	loc := weather.Location
	forecast := weather.Forecast

	var sb strings.Builder

	// Header with location and time
	sb.WriteString(fmt.Sprintf("**%s, %s**\n", loc.Name, loc.Country))
	sb.WriteString(fmt.Sprintf("Coordinates: %.2f, %.2f\n", loc.Lat, loc.Lon))
	sb.WriteString(fmt.Sprintf("Local Time: %s\n\n", loc.Localtime))

	// Forecast section
	sb.WriteString(fmt.Sprintf("**%d-Day Weather Forecast:**\n\n", len(forecast.Forecastday)))

	for i, day := range forecast.Forecastday {
		date, _ := time.Parse("2006-01-02", day.Date)

		// Day header
		if i == 0 {
			sb.WriteString(fmt.Sprintf("**Today** (%s)\n", date.Format("Monday, January 2")))
		} else {
			sb.WriteString(fmt.Sprintf("**%s** (%s)\n", date.Format("Monday"), date.Format("January 2")))
		}

		// Weather details
		sb.WriteString(fmt.Sprintf("   **High:** %.1f°C (%.1f°F) | **Low:** %.1f°C (%.1f°F)\n",
			day.Day.MaxtempC, day.Day.MaxtempF, day.Day.MintempC, day.Day.MintempF))
		sb.WriteString(fmt.Sprintf("   **Conditions:** %s\n", day.Day.Condition.Text))
		sb.WriteString(fmt.Sprintf("   **Wind:** %.1f km/h (%.1f mph)\n", day.Day.MaxwindKph, day.Day.MaxwindMph))
		sb.WriteString(fmt.Sprintf("   **Precipitation:** %.1f mm (%.1f in)\n\n", day.Day.TotalprecipMm, day.Day.TotalprecipIn))
	}

	return sb.String()
}
