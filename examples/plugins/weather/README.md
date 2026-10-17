# Weather HTTP example

The Go and Python fixtures implement `.weather <city name>` or
`.weather <latitude> <longitude>` with a
read-only `http` capability. Requests go through `host.http`; neither fixture
opens a socket or uses a language HTTP library. The example allowlists only
the HTTPS Open-Meteo forecast and geocoding origins, caps each response at 64
KiB, disables redirects, and uses a bounded per-chat cooldown. City names are
resolved through Open-Meteo's geocoding endpoint before the forecast request.

The endpoint is a credential-free fixture. A credentialed provider should use a
typed secret/config path and `network.http.write` only when the request method
actually sends data; tokens must not be placed in URLs or logs.

The reply is formatted as a readable multi-line summary with the condition,
temperature, feels-like temperature, humidity, wind speed, update time, and
weather emojis.
