"""Deterministic-shape weather example using only the host HTTP SDK."""

from __future__ import annotations

import time
from math import isfinite
from typing import ClassVar
from urllib.parse import urlencode

from serenox_sdk import Module
from serenox_sdk.actions import Message


def _weather_summary(code: int) -> tuple[str, str]:
    if code == 0:
        return "☀️", "Ясно"
    if code == 1:
        return "🌤️", "Преимущественно ясно"
    if code == 2:
        return "⛅", "Переменная облачность"
    if code == 3:
        return "☁️", "Пасмурно"
    if code in {45, 48}:
        return "🌫️", "Туман"
    if code in {51, 53, 55, 56, 57}:
        return "🌦️", "Морось"
    if code in {61, 63, 65, 66, 67, 80, 81, 82}:
        return "🌧️", "Дождь"
    if code in {71, 73, 75, 77, 85, 86}:
        return "❄️", "Снег"
    if code in {95, 96, 99}:
        return "⛈️", "Гроза"
    return "🌡️", "Состояние неизвестно"


class Weather(Module):
    id = "example.weather_python"
    version = "1.0.0"
    author = "Serenox"
    description = "Bounded read-only weather lookup"
    strings: ClassVar[dict[str, str]] = {"name": "Weather"}

    def __init__(self) -> None:
        self._cooldown: dict[int, float] = {}

    async def _coordinates(self, city: str) -> tuple[float, float, str]:
        if self.http is None:
            raise ValueError("weather service unavailable")
        query = urlencode(
            {"name": city, "count": 1, "language": "en", "format": "json"}
        )
        response = await self.http.request(
            "GET",
            f"https://geocoding-api.open-meteo.com/v1/search?{query}",
            headers={"accept": "application/json"},
            timeout=10,
            max_response_bytes=64 * 1024,
        )
        if not 200 <= response.status < 300:
            raise ValueError("geocoding status")
        payload = response.json(max_bytes=64 * 1024)
        results = payload.get("results") if isinstance(payload, dict) else None
        if not isinstance(results, list) or not results:
            raise LookupError("city not found")
        result = results[0]
        if not isinstance(result, dict):
            raise TypeError("malformed geocoding data")
        latitude = float(result["latitude"])
        longitude = float(result["longitude"])
        if (
            not isfinite(latitude)
            or not isfinite(longitude)
            or not -90 <= latitude <= 90
            or not -180 <= longitude <= 180
        ):
            raise ValueError("invalid geocoding coordinates")
        name = result.get("name")
        return latitude, longitude, name.strip() if isinstance(name, str) else city

    async def weathercmd(self, message: Message) -> None:
        parts = message.text.split()
        if len(parts) < 2:
            await message.reply(
                "Usage: .weather <city name> or .weather <latitude> <longitude>"
            )
            return
        location_name = ""
        coordinates = len(parts) == 3
        if coordinates:
            try:
                latitude, longitude = float(parts[1]), float(parts[2])
            except ValueError:
                coordinates = False
            else:
                if (
                    not isfinite(latitude)
                    or not isfinite(longitude)
                    or not -90 <= latitude <= 90
                    or not -180 <= longitude <= 180
                ):
                    await message.reply(
                        "Invalid location. Use a city name or latitude and longitude."
                    )
                    return
        if not coordinates:
            city = " ".join(parts[1:]).strip()
            if (
                not city
                or len(city) > 128
                or any(character in city for character in "\r\n")
            ):
                await message.reply(
                    "Invalid location. Use a city name or latitude and longitude."
                )
                return
            if self.http is None:
                await message.reply("Weather service is unavailable.")
                return
            try:
                latitude, longitude, location_name = await self._coordinates(city)
            except LookupError:
                await message.reply("City not found.")
                return
            except (KeyError, TypeError, ValueError):
                await message.reply("Weather service is temporarily unavailable.")
                return
        now = time.monotonic()
        previous = self._cooldown.get(message.chat_id, 0.0)
        if now - previous < 10:
            await message.reply("Please wait before requesting weather again.")
            return
        if len(self._cooldown) >= 256:
            self._cooldown.pop(next(iter(self._cooldown)))
        self._cooldown[message.chat_id] = now
        if self.http is None:
            await message.reply("Weather service is unavailable.")
            return
        url = (
            "https://api.open-meteo.com/v1/forecast"
            f"?{urlencode({'latitude': f'{latitude:.4f}', 'longitude': f'{longitude:.4f}', 'current': 'temperature_2m,apparent_temperature,relative_humidity_2m,wind_speed_10m,weather_code', 'timezone': 'UTC'})}"
        )
        try:
            response = await self.http.request(
                "GET",
                url,
                headers={"accept": "application/json"},
                timeout=10,
                max_response_bytes=64 * 1024,
            )
            if not 200 <= response.status < 300:
                raise ValueError("upstream status")
            payload = response.json(max_bytes=64 * 1024)
            current = payload["current"]
            temperature = float(current["temperature_2m"])
            feels_like = float(current["apparent_temperature"])
            humidity = float(current["relative_humidity_2m"])
            wind_speed = float(current["wind_speed_10m"])
            weather_code = int(current["weather_code"])
            timestamp = str(current["time"])
        except (KeyError, TypeError, ValueError):
            await message.reply("Weather provider returned an error or malformed data.")
            return
        emoji, condition = _weather_summary(weather_code)
        title = (
            f"Погода в {location_name}" if location_name else "Погода по координатам"
        )
        await message.reply(
            f"📍 {title}\n\n"
            f"{emoji} {condition}\n"
            f"🌡️ Температура: {temperature:.1f} °C\n"
            f"🤗 Ощущается как: {feels_like:.1f} °C\n"
            f"💧 Влажность: {humidity:.0f}%\n"
            f"💨 Ветер: {wind_speed:.1f} км/ч\n"
            f"🕒 Обновлено: {timestamp} UTC"
        )
