// Command weather demonstrates a read-only, bounded host HTTP request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

type weatherHandler struct {
	mu       sync.Mutex
	lastChat map[int64]time.Time
	baseURL  string
}

const geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"

type weatherLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name"`
}

func weatherSummary(code int) (string, string) {
	switch code {
	case 0:
		return "☀️", "Ясно"
	case 1:
		return "🌤️", "Преимущественно ясно"
	case 2:
		return "⛅", "Переменная облачность"
	case 3:
		return "☁️", "Пасмурно"
	case 45, 48:
		return "🌫️", "Туман"
	case 51, 53, 55, 56, 57:
		return "🌦️", "Морось"
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return "🌧️", "Дождь"
	case 71, 73, 75, 77, 85, 86:
		return "❄️", "Снег"
	case 95, 96, 99:
		return "⛈️", "Гроза"
	default:
		return "🌡️", "Состояние неизвестно"
	}
}

var (
	errInvalidLocation  = errors.New("invalid location")
	errLocationNotFound = errors.New("location not found")
)

func newWeatherHandler() *weatherHandler {
	return &weatherHandler{lastChat: make(map[int64]time.Time), baseURL: "https://api.open-meteo.com/v1/forecast"}
}

func (h *weatherHandler) Metadata(context.Context) (protocol.PluginMetadata, error) {
	return protocol.PluginMetadata{ID: "example.weather", Name: "Weather", Version: "1.1.0", Description: "Bounded weather lookup through Serenox host HTTP", Author: "Serenox", ProtocolVersion: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityCommands, protocol.CapabilityHTTP}, Commands: []protocol.CommandSpec{{Name: "weather", Description: "Show weather for a city or latitude and longitude"}}, Config: []protocol.ConfigSpec{{Key: "weather_base_url", Type: protocol.ConfigString, Description: "Approved weather API endpoint", Default: json.RawMessage(`"https://api.open-meteo.com/v1/forecast"`)}}}, nil
}

func (h *weatherHandler) Configure(_ context.Context, values []protocol.ConfigValue) error {
	for _, value := range values {
		if value.Key != "weather_base_url" {
			continue
		}
		var raw string
		if err := json.Unmarshal(value.Value, &raw); err != nil {
			return fmt.Errorf("weather endpoint must be a string")
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host != "api.open-meteo.com" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("weather endpoint must be the approved HTTPS origin")
		}
		h.baseURL = raw
	}
	return nil
}

func (h *weatherHandler) Start(context.Context) error { return nil }
func (h *weatherHandler) Stop(context.Context) error  { return nil }
func (h *weatherHandler) Health(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error) {
	return protocol.HealthResponse{Ready: true, Status: "ok"}, nil
}

func (h *weatherHandler) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if request.Command == nil || request.Command.Name != "weather" {
		return protocol.DispatchResponse{}, nil
	}
	if len(request.Command.Args) == 0 {
		return h.reply(request.Command.Message, "Usage: .weather <city name> or .weather <latitude> <longitude>"), nil
	}
	h.mu.Lock()
	previous := h.lastChat[request.Command.Message.ChatID]
	if time.Since(previous) < 10*time.Second {
		h.mu.Unlock()
		return h.reply(request.Command.Message, "Please wait before requesting weather again."), nil
	}
	if len(h.lastChat) >= 256 {
		for key := range h.lastChat {
			delete(h.lastChat, key)
			break
		}
	}
	h.lastChat[request.Command.Message.ChatID] = time.Now()
	h.mu.Unlock()
	client := serenox.HTTPClientFromContext(ctx)
	if client == nil {
		return h.reply(request.Command.Message, "Weather service is unavailable."), nil
	}
	lat, lon, locationName, err := h.location(ctx, client, request.Command.Args)
	if err != nil {
		if errors.Is(err, errLocationNotFound) {
			return h.reply(request.Command.Message, "City not found."), nil
		}
		if errors.Is(err, errInvalidLocation) {
			return h.reply(request.Command.Message, "Invalid location. Use a city name or latitude and longitude."), nil
		}
		return h.reply(request.Command.Message, "Weather service is temporarily unavailable."), nil
	}
	h.mu.Lock()
	base := h.baseURL
	h.mu.Unlock()
	query := url.Values{"latitude": {strconv.FormatFloat(lat, 'f', 4, 64)}, "longitude": {strconv.FormatFloat(lon, 'f', 4, 64)}, "current": {"temperature_2m,weather_code"}, "timezone": {"UTC"}}
	response, err := client.Request(ctx, protocol.HTTPRequest{Method: "GET", URL: base + "?" + query.Encode(), MaxResponseBytes: 64 << 10, TimeoutMS: 10_000})
	if err != nil {
		return h.reply(request.Command.Message, "Weather service is temporarily unavailable."), nil
	}
	if response.Status < 200 || response.Status >= 300 {
		return h.reply(request.Command.Message, "Weather provider returned an error."), nil
	}
	var payload struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			FeelsLike   float64 `json:"apparent_temperature"`
			Humidity    float64 `json:"relative_humidity_2m"`
			WindSpeed   float64 `json:"wind_speed_10m"`
			WeatherCode int     `json:"weather_code"`
			Time        string  `json:"time"`
		} `json:"current"`
	}
	if err := response.JSON(64<<10, &payload); err != nil || payload.Current.Time == "" {
		return h.reply(request.Command.Message, "Weather provider returned malformed data."), nil
	}
	emoji, condition := weatherSummary(payload.Current.WeatherCode)
	title := "Погода по координатам"
	if locationName != "" {
		title = "Погода в " + locationName
	}
	return h.reply(request.Command.Message, fmt.Sprintf(
		"📍 %s\n\n%s %s\n🌡️ Температура: %.1f °C\n🤗 Ощущается как: %.1f °C\n💧 Влажность: %.0f%%\n💨 Ветер: %.1f км/ч\n🕒 Обновлено: %s UTC",
		title, emoji, condition, payload.Current.Temperature, payload.Current.FeelsLike,
		payload.Current.Humidity, payload.Current.WindSpeed, payload.Current.Time,
	)), nil
}

func (h *weatherHandler) location(ctx context.Context, client *serenox.HTTPClient, args []string) (float64, float64, string, error) {
	if len(args) == 2 {
		lat, latErr := strconv.ParseFloat(args[0], 64)
		lon, lonErr := strconv.ParseFloat(args[1], 64)
		if latErr == nil && lonErr == nil {
			if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				return 0, 0, "", errInvalidLocation
			}
			return lat, lon, "", nil
		}
	}

	city := strings.TrimSpace(strings.Join(args, " "))
	if city == "" || len([]rune(city)) > 128 || strings.ContainsAny(city, "\r\n") {
		return 0, 0, "", errInvalidLocation
	}
	query := url.Values{"name": {city}, "count": {"1"}, "language": {"en"}, "format": {"json"}}
	response, err := client.Request(ctx, protocol.HTTPRequest{Method: "GET", URL: geocodingURL + "?" + query.Encode(), MaxResponseBytes: 64 << 10, TimeoutMS: 10_000})
	if err != nil {
		return 0, 0, "", err
	}
	if response.Status < 200 || response.Status >= 300 {
		return 0, 0, "", err
	}
	var payload struct {
		Results []weatherLocation `json:"results"`
	}
	if err := response.JSON(64<<10, &payload); err != nil {
		return 0, 0, "", err
	}
	if len(payload.Results) == 0 {
		return 0, 0, "", errLocationNotFound
	}
	location := payload.Results[0]
	if math.IsNaN(location.Latitude) || math.IsNaN(location.Longitude) || math.IsInf(location.Latitude, 0) || math.IsInf(location.Longitude, 0) || location.Latitude < -90 || location.Latitude > 90 || location.Longitude < -180 || location.Longitude > 180 {
		return 0, 0, "", errInvalidLocation
	}
	return location.Latitude, location.Longitude, strings.TrimSpace(location.Name), nil
}

func (h *weatherHandler) reply(message protocol.Message, text string) protocol.DispatchResponse {
	return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "weather-reply", Kind: protocol.ActionReply, ChatID: message.ChatID, ReplyToMessageID: message.ID, Text: strings.TrimSpace(text)}}}
}

func main() {
	if err := serenox.Run(context.Background(), newWeatherHandler(), serenox.Config{Workers: 1, QueueSize: 8}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
