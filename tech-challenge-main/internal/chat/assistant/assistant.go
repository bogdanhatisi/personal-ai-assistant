package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/acai-travel/tech-challenge/internal/chat/model"
	ics "github.com/arran4/golang-ical"
	"github.com/openai/openai-go/v2"
)

// isWeatherQuery checks if a message is asking about weather
func isWeatherQuery(content string) bool {
	content = strings.ToLower(content)
	weatherKeywords := []string{
		"weather", "temperature", "forecast", "climate", "hot", "cold", "rain", "snow",
		"sunny", "cloudy", "wind", "humidity", "°c", "°f", "celsius", "fahrenheit",
	}

	for _, keyword := range weatherKeywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

type Assistant struct {
	cli            openai.Client
	weatherService *WeatherService
}

func New() *Assistant {
	weatherAPIKey := os.Getenv("WEATHER_API_KEY")
	var weatherService *WeatherService
	if weatherAPIKey != "" {
		weatherService = NewWeatherService(weatherAPIKey)
	}

	return &Assistant{
		cli:            openai.NewClient(),
		weatherService: weatherService,
	}
}

func (a *Assistant) Title(ctx context.Context, conv *model.Conversation) (string, error) {
	if len(conv.Messages) == 0 {
		return "An empty conversation", nil
	}

	slog.InfoContext(ctx, "Generating title for conversation", "conversation_id", conv.ID)

	systemPrompt := `You are a title generator.

TASK
- Return ONLY a short, descriptive title for the conversation/topic.

FORMAT
- Output exactly one line with the title text. No quotes, no code blocks, no extra words.
- Maximum 80 characters.
- No emojis or unusual symbols.
- Do NOT answer the question or explain anything.

SPECIAL CASE
- If the conversation is empty, return: An empty conversation

EXAMPLES
User: What is the weather like in Barcelona?
You: Weather in Barcelona

User: How do I add items to a list in Python?
You: Python list methods

User: Tell me the steps to set up a Postgres replica
You: Setting up a PostgreSQL replica`

	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}

	for _, m := range conv.Messages {
		switch m.Role {
		case model.RoleUser:
			msgs = append(msgs, openai.UserMessage(m.Content))
		case model.RoleAssistant:
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		}
	}

	resp, err := a.cli.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModelO1,
		Messages: msgs,
	})

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "", errors.New("empty response from OpenAI for title generation")
	}

	title := resp.Choices[0].Message.Content
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.Trim(title, " \t\r\n-\"'")

	if len(title) > 80 {
		title = title[:80]
	}

	return title, nil
}

func (a *Assistant) Reply(ctx context.Context, conv *model.Conversation) (string, error) {
	if len(conv.Messages) == 0 {
		return "", errors.New("conversation has no messages")
	}

	slog.InfoContext(ctx, "Generating reply for conversation", "conversation_id", conv.ID)

	// Log weather service status
	if a.weatherService != nil {
		slog.InfoContext(ctx, "Weather service is configured and available")
	} else {
		slog.WarnContext(ctx, "Weather service is NOT configured - WEATHER_API_KEY may not be set")
	}

	// NOTE: We no longer intercept weather queries or try to guess the location here.
	// All weather-related requests are handled via the get_weather tool to avoid
	// brittle heuristics and ensure the model extracts location + forecast_days.

	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(`You are a helpful AI assistant with access to specialized tools.

WEATHER – TOOL USE
1) Always call **get_weather** for weather/temperature/forecast/climate questions. Never invent weather.
2) Args for get_weather:
   • **location**: extract from the user message (city, "City,Country", or "lat,lon").
   • **forecast_days**:
     – If the user asks for a specific **weekday or date** (e.g., "Friday", "Sep 5"), first call **get_today_date**, compute the day difference from today, then set **forecast_days = diff + 1** (clamp 1–10). After receiving data, answer **only for that target day** (not the whole range).
     – Otherwise, default to a **short forecast** (1–3 days). Do NOT request 7+ days unless explicitly asked.
   • If the location is missing or ambiguous, ask one brief clarifying question.

RESPONSE STYLE (IMPORTANT)
3) Write a concise, readable answer tailored to the user’s request. Do **not** just echo tool output.
   • Start with a single line header: **<City, Country> — <Day label>** (e.g., **Barcelona, Spain — Friday**).
   • Then 3–5 short bullet points covering:
     – Conditions (e.g., Sunny / Light rain).
     – Temperatures: High/Low in °C (add °F only if the user used °F).
     – Rain chance/precip if available; otherwise omit.
     – Wind (speed + direction if available).
   • Keep numbers clean (no excessive decimals). Avoid long paragraphs.
   • If the user specifies part of day (e.g., "morning"), focus the summary on that period; if hourly detail isn’t available, state what’s most likely and include the day’s range.

OTHER TOOLS
4) Use **get_today_date** for current date/time questions.
5) Use **get_holidays** for holiday/calendar questions.
6) For non-tool queries, answer normally.`),
	}

	for _, m := range conv.Messages {
		switch m.Role {
		case model.RoleUser:
			// Force function usage for weather-related queries
			content := m.Content
			if isWeatherQuery(content) {
				content = "IMPORTANT: You MUST use the get_weather function to answer this question. Do NOT generate weather information from your training data. Extract the location and forecast_days (if any) from the user's text. Question: " + content
				slog.InfoContext(ctx, "Weather query detected, forcing function usage", "original", m.Content, "modified", content)
			}
			msgs = append(msgs, openai.UserMessage(content))
		case model.RoleAssistant:
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		}
	}

	for i := 0; i < 15; i++ {
		resp, err := a.cli.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:    openai.ChatModelO1,
			Messages: msgs,
			Tools: []openai.ChatCompletionToolUnionParam{
				openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("ALWAYS use this function when users ask about weather, temperature, forecast, or climate conditions. Do NOT generate weather information from training data. This function provides real-time weather data from WeatherAPI."),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]string{
								"type":        "string",
								"description": "City name, coordinates, or location query (e.g., 'Barcelona', 'London,UK', '40.7128,-74.0060')",
							},
							"forecast_days": map[string]any{
								"type":        "integer",
								"description": "Number of forecast days (1-14). If not provided, returns only current weather.",
							},
						},
						"required": []string{"location"},
					},
				}),
				openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
					Name:        "get_today_date",
					Description: openai.String("Get today's date and time in RFC3339 format"),
				}),
				openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
					Name:        "get_holidays",
					Description: openai.String("Gets local bank and public holidays. Each line is a single holiday in the format 'YYYY-MM-DD: Holiday Name'."),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]any{
							"before_date": map[string]string{
								"type":        "string",
								"description": "Optional date in RFC3339 format to get holidays before this date. If not provided, all holidays will be returned.",
							},
							"after_date": map[string]string{
								"type":        "string",
								"description": "Optional date in RFC3339 format to get holidays after this date. If not provided, all holidays will be returned.",
							},
							"max_count": map[string]string{
								"type":        "integer",
								"description": "Optional maximum number of holidays to return. If not provided, all holidays will be returned.",
							},
						},
					},
				}),
			},
		})

		if err != nil {
			return "", err
		}

		if len(resp.Choices) == 0 {
			return "", errors.New("no choices returned by OpenAI")
		}

		if message := resp.Choices[0].Message; len(message.ToolCalls) > 0 {
			slog.InfoContext(ctx, "Tool calls detected", "count", len(message.ToolCalls))
			msgs = append(msgs, message.ToParam())

			for _, call := range message.ToolCalls {
				slog.InfoContext(ctx, "Tool call received", "name", call.Function.Name, "args", call.Function.Arguments)

				switch call.Function.Name {
				case "get_weather":
					var payload struct {
						Location     string `json:"location"`
						ForecastDays *int   `json:"forecast_days,omitempty"`
					}

					if err := json.Unmarshal([]byte(call.Function.Arguments), &payload); err != nil {
						msgs = append(msgs, openai.ToolMessage("failed to parse weather request arguments: "+err.Error(), call.ID))
						break
					}

					if a.weatherService == nil {
						msgs = append(msgs, openai.ToolMessage("Weather service is not configured. Please set WEATHER_API_KEY environment variable.", call.ID))
						break
					}

					var weatherInfo string
					var err error

					if payload.ForecastDays != nil && *payload.ForecastDays > 0 {
						weatherInfo, err = a.weatherService.GetForecast(ctx, payload.Location, *payload.ForecastDays)
					} else {
						weatherInfo, err = a.weatherService.GetCurrentWeather(ctx, payload.Location)
					}

					if err != nil {
						msgs = append(msgs, openai.ToolMessage("Failed to get weather information: "+err.Error(), call.ID))
						break
					}

					msgs = append(msgs, openai.ToolMessage(weatherInfo, call.ID))
				case "get_today_date":
					msgs = append(msgs, openai.ToolMessage(time.Now().Format(time.RFC3339), call.ID))
				case "get_holidays":
					link := "https://www.officeholidays.com/ics/spain/catalonia"
					if v := os.Getenv("HOLIDAY_CALENDAR_LINK"); v != "" {
						link = v
					}

					events, err := LoadCalendar(ctx, link)
					if err != nil {
						msgs = append(msgs, openai.ToolMessage("failed to load holiday events", call.ID))
						break
					}

					var payload struct {
						BeforeDate time.Time `json:"before_date,omitempty"`
						AfterDate  time.Time `json:"after_date,omitempty"`
						MaxCount   int       `json:"max_count,omitempty"`
					}

					if err := json.Unmarshal([]byte(call.Function.Arguments), &payload); err != nil {
						msgs = append(msgs, openai.ToolMessage("failed to parse tool call arguments: "+err.Error(), call.ID))
						break
					}

					var holidays []string
					for _, event := range events {
						date, err := event.GetAllDayStartAt()
						if err != nil {
							continue
						}

						if payload.MaxCount > 0 && len(holidays) >= payload.MaxCount {
							break
						}

						if !payload.BeforeDate.IsZero() && date.After(payload.BeforeDate) {
							continue
						}

						if !payload.AfterDate.IsZero() && date.Before(payload.AfterDate) {
							continue
						}

						holidays = append(holidays, date.Format(time.DateOnly)+": "+event.GetProperty(ics.ComponentPropertySummary).Value)
					}

					msgs = append(msgs, openai.ToolMessage(strings.Join(holidays, "\n"), call.ID))
				default:
					return "", errors.New("unknown tool call: " + call.Function.Name)
				}
			}

			continue
		}

		// Log when no tool calls are made
		if len(resp.Choices[0].Message.ToolCalls) == 0 {
			slog.InfoContext(ctx, "No tool calls made - OpenAI generated direct response", "content_length", len(resp.Choices[0].Message.Content))
		}

		return resp.Choices[0].Message.Content, nil
	}

	return "", errors.New("too many tool calls, unable to generate reply")
}
